#!/usr/bin/env bash

set -e

if [ $# -lt 1 ]
then
    echo "One argument required: Path to configs"
    exit 1
fi
commit_hash=$(git rev-parse --short HEAD)
abbrev_len=$(echo $commit_hash | wc -c | xargs)
echo "Warning: Content of the /test/k8s/config will be overwritten!"

read -p "Proceed? " -n 1 -r
if [[ $REPLY =~ ^[Yy]$ ]]
then
    cp -rf $1/config/prod/* ./test/k8s/config
    config_hash_prod=$(cd test/k8s && go run github.com/bookingcom/nanotube/cmd/nanotube -config "$PWD/config/config.toml" -confighash 1 | cut "-c-$abbrev_len")
    echo "$config_hash_prod"
    image_prod=nanokube:$commit_hash\_$config_hash_prod\_prod
    echo "Building image $image_prod from configs in $1/config."
    docker build -t "docker.jfrog.booking.com/projects/nanokube/$image_prod" -f test/k8s/nt.Dockerfile ./.
    docker push "docker.jfrog.booking.com/projects/nanokube/$image_prod"

    cp -rf $1/config/dqs/* ./test/k8s/config
    config_hash_dqs=$(cd test/k8s && go run github.com/bookingcom/nanotube/cmd/nanotube -config "$PWD/config/config.toml" -confighash 1 | cut "-c-$abbrev_len")
    image_dqs=nanokube:$commit_hash\_$config_hash_dqs\_dqs
    echo "Building image $image_dqs from configs in $1/config."
    docker build -t "docker.jfrog.booking.com/projects/nanokube/$image_dqs" -f test/k8s/nt.Dockerfile ./.
    docker push "docker.jfrog.booking.com/projects/nanokube/$image_dqs"
fi
