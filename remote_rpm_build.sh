#!/usr/bin/env bash

set -o errexit   # abort on nonzero exitstatus
set -o nounset   # abort on unbound variable
set -o pipefail  # don't hide errors within pipes
set -x

if [ $# -le 1 ]; then
    echo 'Error: Version arguments not supplied'
    exit 1
fi

if ! [[ $1 =~ [0-9]{8} ]]; then
    echo 'Error: Version date does not have an appropriate form'
    exit 1
fi

if ! [[ $2 =~ [0-9]{6} ]]; then
    echo 'Error: Version time does not have an appropriate form'
    exit 1
fi

cd ~/git_tree/packages/subnanotube
echo "Updating the spec file."
sed "s/_git_tag nanotube-[0-9]\{8\}-[0-9]\{6\}/_git_tag nanotube-$1-$2/" <subnanotube.spec >subnanotube.spec_
sed -i "s/_version [0-9]\{8\}.[0-9]\{6\}/_version ${1}.${2}/" subnanotube.spec_
echo "Updated spec file diff:"
diff subnanotube.spec subnanotube.spec_ || true

read -n 1 -p "Proceed (y/n)? " answer
if [ "$answer" != 'y' ]; then
    rm subnanotube.spec_
    exit 1
fi

mv subnanotube.spec_ subnanotube.spec

echo "Building packages."

bpackage-adm regular --tgzfromgit subnanotube
bpackage-adm regular --build subnanotube --centos 7
bpackage-adm regular --build subnanotube --centos 8
