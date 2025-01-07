%global _enable_debug_package 0
%global __os_install_post /usr/lib/rpm/brp-compress %{nil}

%define debug_package %{nil}
%define gitlab_path gitlab.booking.com/graphite

%define _name       subnanotube
# read version from $CI_COMMIT_TAG or create a dummy one if not
# available
%if "%{getenv:RPMBUILD_CI_COMMIT_TAG}" == ""
%define _version dummy
%else
%define _version %{getenv:RPMBUILD_CI_COMMIT_TAG}
%endif

Name:           %{_name}
Version:        %{_version}
Release:	1%{?dist}
Summary:        Metrics router for graphite
Group:          Applications/Internet
License:        Apache License 2.0
URL:            https://gitlab.booking.com/graphite/nanotube
Source0:        %{_name}-%{version}.tar.gz
BuildRoot:      %{_tmppath}/%{name}-%{version}-%{release}-root-%(%{__id_u} -n)
BuildArch:      x86_64
BuildRequires:  golang >= 1.23
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
go build -ldflags "-X main.version=%{version}" ./cmd/nanotube

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


%changelog
* Mon Jul 19 2021 <nicolas.lannuzel@booking.com>
- Switch to docker-rpmbuild piepeline in Gitlab

* Tue Jul 06 2021 <xiaofan.hu@booking.com> - 20210706.133517
- Built by bpackaged from git tag: subnanotube-20210706-133517, build id: 84707

* Thu Jun 17 2021 <roman.grytskiv@booking.com> - 20210506.152947
- Built by bpackaged from git tag: nanotube-20210506-152947, build id: 84307

* Thu May 06 2021 <roman.grytskiv@booking.com> - 20210506.152947
- Built by bpackaged from git tag: nanotube-20210506-152947, build id: 83444

* Wed Mar 03 2021 <roman.grytskiv@booking.com> - 20210303.173340
- Built by bpackaged from git tag: nanotube-20210303-173340, build id: 81404

* Tue Mar 02 2021 <roman.grytskiv@booking.com> - 20210302.173031_aborted
- Built by bpackaged from git tag: nanotube-20210302-173031_aborted, build id: 81365

* Tue Mar 02 2021 <roman.grytskiv@booking.com> - 20210302.173031
- Built by bpackaged from git tag: nanotube-20210302-173031, build id: 81363

* Fri Jan 15 2021 <damien.krotkine@booking.com> - 20210115.090156
- Built by bpackaged from git tag: subnanotube-20210115-090156, build id: 79846

* Thu Jan 14 2021 <damien.krotkine@booking.com> - 20210114183900
- Built by bpackaged from git tag: subnanotube-20210114183900, build id: 79842

* Wed Jan 13 2021 <damien.krotkine@booking.com> - 20210113211900
- Built by bpackaged from git tag: nanotube-20210113211900, build id: 79706

* Wed Jan 13 2021 <damien.krotkine@booking.com> - 20210113152204
- Built by bpackaged from git tag: nanotube-20210113152204, build id: 79689

* Wed Jan 13 2021 <damien.krotkine@booking.com> - 20210113152203
- Built by bpackaged from git tag: nanotube-20210113152203, build id: 79683

* Wed Jan 13 2021 <damien.krotkine@booking.com> - 20210113152202
- Built by bpackaged from git tag: nanotube-20210113152202, build id: 79681

* Fri Oct 09 2020 <roman.grytskiv@booking.com> - 20201009.151726
- Built by bpackaged from git tag: nanotube-20201009-151726, build id: 75154

* Thu Jul 30 2020 <roman.grytskiv@booking.com> - 20200730.175636
- Built by bpackaged from git tag: nanotube-20200730-175636, build id: 70465

* Thu Jul 30 2020 <roman.grytskiv@booking.com> - 20200730.161007
- Built by bpackaged from git tag: nanotube-20200730-161007, build id: 70442

* Thu Jun 25 2020 <gyanendra.singh@booking.com> - 20200625.141200
- Built by bpackaged from git tag: nanotube-20200625-141200, build id: 68289

* Thu Jun 25 2020 <gyanendra.singh@booking.com> - 20200625.122606
- Built by bpackaged from git tag: nanotube-20200625-122606, build id: 68249

* Fri Jun 19 2020 <gyanendra.singh@booking.com> - 20200619.170806
- Built by bpackaged from git tag: nanotube-20200619-170806, build id: 67827

* Fri Jun 19 2020 <gyanendra.singh@booking.com> - 20200619.154806
- Built by bpackaged from git tag: nanotube-20200619-154806, build id: 67800

* Fri Jun 05 2020 <andrei.vereha@booking.com> - 20200605.111641
- Built by bpackaged from git tag: nanotube-20200605-111641, build id: 66603

* Fri Jun 05 2020 <andrei.vereha@booking.com> - 20200605.111351_aborted
- Built by bpackaged from git tag: nanotube-20200605-111351_aborted, build id: 66594

* Thu Jun 04 2020 <alexey.zhiltsov@booking.com> - 20200604.151415
- Built by bpackaged from git tag: nanotube-20200604-151415, build id: 66525

* Thu Jun 04 2020 <gyanendra.singh@booking.com> - 20200604.135335
- Built by bpackaged from git tag: nanotube-20200604-135335, build id: 66521

* Wed May 20 2020 <alexey.zhiltsov@booking.com> - 20200520.102202
- Built by bpackaged from git tag: subnanotube-20200520-102202, build id: 65329

* Tue May 19 2020 <alexey.zhiltsov@booking.com> - 20200519.173905
- Built by bpackaged from git tag: subnanotube-20200519-173905, build id: 65228

* Mon May 18 2020 <gyanendra.singh@booking.com> - 20200518.153205
- Built by bpackaged from git tag: subnanotube-20200518-153205, build id: 65088

* Mon May 18 2020 <nicolas.lannuzel@booking.com> - 20200513.152403
- Built by bpackaged from git tag: subnanotube-20200513-152403, build id: 65067

* Mon May 18 2020 <nicolas.lannuzel@booking.com> - 20200513.152403
- Built by bpackaged from git tag: subnanotube-20200513-152403, build id: 64921

* Mon May 18 2020 <gyanendra.singh@booking.com> - 20200518.012705
- Built by bpackaged from git tag: subnanotube-20200518-012705, build id: 64920

* Mon May 18 2020 <gyanendra.singh@booking.com> - 20200518.012603
- Built by bpackaged from git tag: subnanotube-20200518-012603, build id: 64916

* Wed May 13 2020 <alexey.zhiltsov@booking.com> - 20200513.152403
- Built by bpackaged from git tag: subnanotube-20200513-152403, build id: 64549

* Wed May 13 2020 <gyanendra.singh@booking.com> - 20200513.134302
- Built by bpackaged from git tag: nanotube-20200513-134302, build id: 64501

* Tue May 12 2020 <alexey.zhiltsov@booking.com> - 20200512.152400
- Built by bpackaged from git tag: nanotube-20200512-152400, build id: 64367

* Fri May 08 2020 <roman.grytskiv@booking.com> - 20200508.171824
- Built by bpackaged from git tag: nanotube-20200508-171824, build id: 64183

* Thu May 07 2020 <roman.grytskiv@booking.com> - 20200507.155452
- Built by bpackaged from git tag: nanotube-20200507-155452, build id: 64075

* Thu May 07 2020 <roman.grytskiv@booking.com> - 20200507.152828
- Built by bpackaged from git tag: nanotube-20200507-152828, build id: 64071

* Thu May 07 2020 <roman.grytskiv@booking.com> - 20200507.150856
- Built by bpackaged from git tag: nanotube-20200507-150856, build id: 64065

* Thu May 07 2020 <roman.grytskiv@booking.com> - 20200507.140231
- Built by bpackaged from git tag: nanotube-20200507-140231, build id: 64041

* Wed May 06 2020 <alexey.zhiltsov@booking.com> - 20200506.182915
- Built by bpackaged from git tag: nanotube-20200506-182915, build id: 64006

* Wed May 06 2020 <alexey.zhiltsov@booking.com> - 20200506.182900_reverted
- Built by bpackaged from git tag: nanotube-20200506-182900_reverted, build id: 64003

* Wed May 06 2020 <alexey.zhiltsov@booking.com> - 20200506.145700
- Built by bpackaged from git tag: nanotube-20200506-145700, build id: 63976

* Wed May 06 2020 <alexey.zhiltsov@booking.com> - 20200506.122351
- Built by bpackaged from git tag: nanotube-20200506-122351, build id: 63971

* Wed May 06 2020 <alexey.zhiltsov@booking.com> - 20200506.120642_aborted
- Built by bpackaged from git tag: nanotube-20200506-120642_aborted, build id: 63965

* Wed May 06 2020 <alexey.zhiltsov@booking.com> - 20200506.120642
- Built by bpackaged from git tag: nanotube-20200506-120642, build id: 63964

* Mon May 04 2020 <roman.grytskiv@booking.com> - 20200504.172724
- Built by bpackaged from git tag: nanotube-20200504-172724, build id: 63894

* Wed Apr 29 2020 <roman.grytskiv@booking.com> - 20200429.123422
- Built by bpackaged from git tag: nanotube-20200429-123422, build id: 63635

* Wed Apr 29 2020 <roman.grytskiv@booking.com> - 20200429.120812
- Built by bpackaged from git tag: nanotube-20200429-120812, build id: 63621

* Wed Apr 29 2020 <roman.grytskiv@booking.com> - 20200429.114914
- Built by bpackaged from git tag: nanotube-20200429-114914, build id: 63609

* Wed Apr 22 2020 <gyanendra.singh@booking.com> - 20200422.151100
- Built by bpackaged from git tag: nanotube-20200422-151100, build id: 63034

* Thu Apr 09 2020 <alexey.zhiltsov@booking.com> - 20200409.220200
- Built by bpackaged from git tag: nanotube-20200409-220200, build id: 62598

* Wed Feb 05 2020 <alexey.zhiltsov@booking.com> - 0.01.0
- Initial build

