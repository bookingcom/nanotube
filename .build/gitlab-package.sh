#!/bin/bash -xe

yum install -y jq

echo "Submitting packaging job"

JOB_ID="$(curl -X POST -H 'Content-Type: application/json' -H "X-API-KEY: $PACKAGING_API_KEY" -d "{ \"owner\": \"$GITLAB_USER_EMAIL\", \"release_pkg\": 1, \"pkg_scm_url\": \"git@gitlab.booking.com:$CI_PROJECT_PATH.git\", \"pkg_scm_rev\":\"tag::$CI_COMMIT_TAG\", \"spec_scm_dir\": \"packages.git::$CI_PROJECT_NAME\", \"centos\": \"6,7\" }" http://yum-master.prod.booking.com/api/builds/new?src_from_scm=1 | jq .job_id)"

[[ -z $JOB_ID ]] && (echo "Problem sending build request!!" && exit 1)

echo "Job submitted with id $JOB_ID"

while true; do
    for centos_version in 6 7; do
        if [[ ${done[$centos_version]} -eq 1 ]]; then
            continue
        fi
        echo ===centos${centos_version}===
        response=$(curl -s -X GET -H 'Content-Type: application/json' -H "X-API-KEY: $PACKAGING_API_KEY" http://yum-master.prod.booking.com/api/builds/list/by_key?job_id=$JOB_ID | jq -r '.builds[] | select(.centos == "'${centos_version}'")' )

        jq ' {id: .id, bpackage_log: .bpackage_log, build_state: .build_state}' <<<$response

        [[ $(jq .build_state <<<$response) =~ done|failed ]] && echo ===centos${centos_version}=== finished && done[$centos_version]=1
    done
    [[ ${done[6]} -eq 1 && ${done[7]} -eq 1 ]] && break
    sleep 5
done

response=$(curl -s -X GET -H 'Content-Type: application/json' -H "X-API-KEY: $PACKAGING_API_KEY" http://yum-master.prod.booking.com/api/builds/list/by_key?job_id=$JOB_ID | jq  '.builds[] | {id: .id, bpackage_log: .bpackage_log, build_state: .build_state, centos: .centos, rpms_built: .rpms_built}' | sed -e 's/\/data0/http:\/\/yum-master.prod.booking.com/g')

# check if any job failed
[[ $(jq .build_state <<<$response) =~ failed ]] && (echo "Packaging Failed!!" && exit 1)

echo "Packaging Successful!!"
exit 0
