#!/usr/bin/env bash

set -ex
SCRIPT_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )
cd "$SCRIPT_DIR"

function die() {
  set +x
  me=$(basename $0)
  echo "$(tput setaf 9)${me}: ${*}$(tput sgr0)" 1>&2
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
[[ -n "$commit_hash" ]] || die "failed to extract commit_hash from git"

abbrev_len=$(echo $commit_hash | wc -c | xargs)

echo "Warning: Contents of $PWD/test/k8s/config will be overwritten!"
read -p "Proceed? " -n 1 -r
if [[ $REPLY =~ ^[Yy]$ ]]
then
    set +x
    export DOCKER_DEFAULT_PLATFORM="linux/amd64"

    cp -rvf $nanokube_dir/config/dqs/* $PWD/test/k8s/config
    config_hash_dqs=$(cd $PWD/test/k8s && go run github.com/bookingcom/nanotube/cmd/nanotube -config "$PWD/config/config.toml" -confighash 1 | cut "-c-$abbrev_len")

    [[ -n "$config_hash_dqs" ]] || die "failed to extract config_hash_dqs"

    image_dqs="nanokube:${commit_hash}_${config_hash_dqs}_dqs"
    echo "Building image $image_dqs from configs in $nanokube_dir/config."
    docker build -t "docker.jfrog.booking.com/projects/nanokube/$image_dqs" -f $PWD/test/k8s/nt.Dockerfile ./.
    docker push "docker.jfrog.booking.com/projects/nanokube/$image_dqs"

    cp -rvf $nanokube_dir/config/prod/* $PWD/test/k8s/config
    config_hash_prod=$(cd $PWD/test/k8s && go run github.com/bookingcom/nanotube/cmd/nanotube -config "$PWD/config/config.toml" -confighash 1 | cut "-c-$abbrev_len")

    [[ -n "$config_hash_prod" ]] || die "failed to extract config_hash_prod"

    echo "$config_hash_prod"
    image_prod="nanokube:${commit_hash}_${config_hash_prod}_prod"
    echo "Building image $image_prod from configs in $nanokube_dir/config."
    docker build -t "docker.jfrog.booking.com/projects/nanokube/$image_prod" -f $PWD/test/k8s/nt.Dockerfile ./.
    docker push "docker.jfrog.booking.com/projects/nanokube/$image_prod"
fi
