# Security policy

Please report security vulnerabilities by email to **security@hyperme.sh**. Do not open a public issue for a security problem.

Report other bugs to **bugs@hyperme.sh**.

This project has no release. The tree contains an unreleased proxy that binds loopback unless the operator opts into `listen_mode: network`. Do not put API keys in config files. The proxy keeps keys and prompt bodies out of the measurement journal and the default log. Loopback reachability does not authorize use of an environment provider key: set `CONDUCTOR_PROXY_TOKEN` and send it as `X-Conductor-Token` before that key is attached. `listen_mode: network` refuses to start without that token and without `tls.cert_file` and `tls.key_file`. Keep the key file readable only by the operator. There is no conductor product in the control-plane edge routes and no public conductor hostname; an operator TLS ingress in front of `127.0.0.1:8787` is the other way to reach the proxy. `history.jsonl` stores original prompt tails and is created so only the owner can read it. Client values that start with `hm_dev_`, `hm_rtr_`, `hm_site_`, or `hm_gw_` are not forwarded as renter identity.
