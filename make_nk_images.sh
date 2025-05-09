#!/usr/bin/env bash

set -ex

function die() {
  echo "$*" 1>&2
  exit 1
}

if [ $# -lt 1 ]
then
  die "One argument required: Path to configs (\$HOME/git_tree/graphite/nanokube maybe?)"
fi

nanokube_dir="$1"
if [[ ! -d "$nanokube_dir/config" ]]; then
  die "nanokube repo path $nanokube_dir either does not exist, or does not contain a config subdirectory"
fi

commit_hash=$(git rev-parse --short HEAD)
if [[ -z "$commit_hash" ]]; then
  die "failed to extract commit_hash from git"
fi
abbrev_len=$(echo $commit_hash | wc -c | xargs)
echo "Warning: Content of the /test/k8s/config will be overwritten!"

read -p "Proceed? " -n 1 -r
if [[ $REPLY =~ ^[Yy]$ ]]
then
    export DOCKER_DEFAULT_PLATFORM="linux/amd64"
    cp -rf $nanokube_dir/config/prod/* ./test/k8s/config
    config_hash_prod=$(cd test/k8s && go run github.com/bookingcom/nanotube/cmd/nanotube -config "$PWD/config/config.toml" -confighash 1 | cut "-c-$abbrev_len")

    if [[ -z "$config_hash_prod" ]]; then
      die "failed to extract config_hash_prod"
    fi

    echo "$config_hash_prod"
    image_prod="nanokube:${commit_hash}_${config_hash_prod}_prod"
    echo "Building image $image_prod from configs in $nanokube_dir/config."
    docker build -t "docker.jfrog.booking.com/projects/nanokube/$image_prod" -f test/k8s/nt.Dockerfile ./.
    docker push "docker.jfrog.booking.com/projects/nanokube/$image_prod"

    cp -rf $nanokube_dir/config/dqs/* ./test/k8s/config
    config_hash_dqs=$(cd test/k8s && go run github.com/bookingcom/nanotube/cmd/nanotube -config "$PWD/config/config.toml" -confighash 1 | cut "-c-$abbrev_len")

    if [[ -z "$config_hash_dqs" ]]; then
      die "failed to extract config_hash_dqs"
    fi

    image_dqs="nanokube:${commit_hash}_${config_hash_dqs}_dqs"
    echo "Building image $image_dqs from configs in $nanokube_dir/config."
    docker build -t "docker.jfrog.booking.com/projects/nanokube/$image_dqs" -f test/k8s/nt.Dockerfile ./.
    docker push "docker.jfrog.booking.com/projects/nanokube/$image_dqs"
fi
