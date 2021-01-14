#!/usr/bin/env bash

set -o errexit   # abort on nonzero exitstatus
set -o nounset   # abort on unbound variable
set -o pipefail  # don't hide errors within pipes

tag_day=$(date '+%Y%m%d')
tag_time=$(date '+%H%M%S')

echo "going to create package for version ${tag_day}.${tag_time}"

echo "1. going to merge origin:master in bk-master"

 git config pull.ff only

echo "   fetching all branches"
git fetch

echo "   checking out the bk-master branch"
git checkout bk-master

echo "   trying to merge with master from Github..."
if git merge --no-edit origin/master
then
    echo "   merge complete, pushing changes"
    git push
else
    echo "   merge failed, please resolve any conflicts, commit, and push manually"
    exit 1
fi

echo "2. going to create a new git tag and push it to trigger rpm build"

git_tag="subnanotube-${tag_day}-${tag_time}"
echo "   tagging new release $git_tag"
git tag "$git_tag"
echo "   pushing tag to trigger packaging CI"
git push --tags
echo "all done, check https://gitlab.booking.com/graphite/nanotube/-/pipelines"
