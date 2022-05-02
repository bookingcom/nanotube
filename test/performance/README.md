# Setting up performance testing system

1. Run `make build-for-benchmarking-setup` from the repository root.
2. Change to this directory.
3. Perform preliminary local setup and define the hosts for running the test
    ```
    ansible-playbook presetup.yaml --extra-vars="presetup_sender_host=... presetup_receiver_host=... presetup_nanotube_host=..."`
    ```
4. Get the Prometheus [here](https://github.com/prometheus/prometheus/releases/download/v2.35.0/prometheus-2.35.0.linux-amd64.tar.gz), extract and move the binary to `roles/prometheus/files/prometheus`
4. Setup the system
    ```
    ansible-playbook -i hosts.yaml setup.yaml
    ```
5. Run `./run-sender.sh` on the `sender` host. You can tune the load and its timing in the script.
