#!/usr/bin/env bash

set -o errexit   # abort on nonzero exitstatus
set -o nounset   # abort on unbound variable
set -o pipefail  # don't hide errors within pipes
set -x

tag_day=$(date '+%Y%m%d')
tag_time=$(date '+%H%M%S')

echo ">>> Building version ${tag_day}.${tag_time}"

git_tag="nanotube-${tag_day}-${tag_time}"
echo "Tagging new release $git_tag."
git tag "$git_tag"
git push --tags


echo "Building an RPM on the rpmbuild KVM"
scp remote_rpm_build.sh "$USER@$USER-rpmbuild.dev.booking.com:~"
ssh "$USER@$USER-rpmbuild.dev.booking.com" ~/remote_rpm_build.sh "${tag_day}" "${tag_time}"
