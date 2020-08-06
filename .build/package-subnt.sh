#!/bin/bash
set -e
set -x

NAME=subnanotube
TAG=${CI_COMMIT_TAG}
VERSION=$(echo "$TAG" | cut -d '-' -f 2,3 --output-delimiter '.')

echo "Packaging subnanotube RPM version: $VERSION"

yum install -y rpm-build rpm-devel rpmlint rpm tree

mkdir -p /build/rpmbuild/RPMS/X86_64
mkdir /build/rpmbuild/SOURCES
mkdir /build/rpmbuild/SPECS
mkdir /build/rpmbuild/SRPMS
cd /build/rpmbuild/SPECS

cat > /build/rpmbuild/SPECS/rpm.spec <<END
%global _enable_debug_package 0
%global __os_install_post /usr/lib/rpm/brp-compress %{nil}

%define booking_repo booking-extras
%define debug_package %{nil}
%define gitlab_path gitlab.booking.com/graphite

%define _git_url    git@gitlab.booking.com:graphite/nanotube.git
%define _name       ${NAME}
%define _git_tag ${TAG}
%define _version ${VERSION}

Name:           %{_name}
Version:        %{_version}
Release:        1%{?dist}
Summary:        Metrics router for graphite
Group:          Applications/Internet
License:        Apache License 2.0
URL:            https://gitlab.booking.com/graphite/nanotube
Source0:        %{_name}-%{version}.tar.gz
BuildRoot:      %{_tmppath}/%{name}-%{version}-%{release}-root-%(%{__id_u} -n)
BuildArch:      x86_64
BuildRequires:  golang >= 1.14
BuildRequires:  make
BuildRequires:  git

%if 0%{?rhel} >= 7
Requires:       systemd
%else
Requires:       initscripts
%endif

%description
High-performance router for Graphite

%prep
%setup -q -n %{name}-%{version}

%build
export EXTRA_BUILD_FLAGS="-a"
make

%install
export DONT_STRIP=1

rm -rf $RPM_BUILD_ROOT
mkdir -p $RPM_BUILD_ROOT/usr/bin

install -pD -m 755 nanotube %{buildroot}/usr/bin/%{name}

%clean
rm -rf $RPM_BUILD_ROOT

%files
%defattr(-,root,root,-)
%{_bindir}/%{name}
END

rpmbuild --target X86_64 -bb rpm.spec
tree /root/rpmbuild
RPMFILEPATH="/root/rpmbuild/RPMS/x86_64/${NAME}-${VERSION}.el7.x86_64.rpm"
MD5SUM=$(md5sum "${RPMFILEPATH}" | awk '{print $1}')
rpm -qip "${RPMFILEPATH}"

echo "Uploading RPM file (${RPMFILEPATH}) to the mirror endpoint"
curl -vH "X-API-KEY: ${PACKAGING_API_KEY}" -i \
    -F 'dest=base' \
    -F "md5sum=${MD5SUM}" \
    -F "centos=7" \
    -F file="@${RPMFILEPATH}" http://yum-master.prod.booking.com/api/rpmupload/
