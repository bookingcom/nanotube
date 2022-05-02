# Setting up performance testing system

1. Run `make build-for-benchmarking-setup` from the repository root.
2. Change to this directory.
3. Perform preliminary local setup and define the hosts for running the test
    ```
    ansible-playbook presetup.yaml --extra-vars="presetup_sender_host=... presetup_receiver_host=... presetup_nanotube_host=..."`
    ```
4. Setup the system
    ```
    ansible-playbook -i hosts.yaml setup.yaml
    ```
5. Run `./run-sender.sh` on the `sender` host.
