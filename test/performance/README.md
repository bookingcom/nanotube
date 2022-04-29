# Setting up performance testing system

1. Perform preliminary local setup and define the hosts for running the test
    ```
    ansible-playbook presetup.yaml --extra-vars="sender_host=... receiver_host=... nanotube_host=..."`
    ```
2. Setup the system
    ```
    ansible-playbook -i hosts.yaml setup.yaml
    ```
3. Run `./run-sender.sh` on the `sender` host.
