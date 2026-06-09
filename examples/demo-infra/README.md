# Acme Corp Demo Infrastructure

A realistic, fictional company infrastructure used to demo **Pine** (the modern
AWX / Ansible Tower replacement). Everything here is safe to scan, parse and
run in check mode: all hostnames, IPs and credentials are placeholders
(`CHANGEME-*`).

## Topology

```
                           Internet
                              |
                       VIP 10.0.1.100 (keepalived)
                      /                \
              +---------+          +---------+
              |  lb01   |          |  lb02   |        [lb]  HAProxy + keepalived
              |10.0.1.11|          |10.0.1.12|
              +----+----+          +----+----+
                   \________  _________/
                            \/
          +---------+  +---------+  +---------+
          |  web01  |  |  web02  |  |  web03  |       [web] nginx + acme-shop (gunicorn)
          |10.0.2.11|  |10.0.2.12|  |10.0.2.13|
          +----+----+  +----+----+  +----+----+
               |            |            |
       +-------+------------+------------+--------+
       |                                          |
+-------------+   streaming   +-------------+  +---------+
| db-primary  | ------------> | db-replica  |  | redis01 |   [db] PostgreSQL 16
|  10.0.3.11  |  replication  |  10.0.3.12  |  |10.0.3.21|   [cache] Redis
+-------------+               +-------------+  +---------+

+---------+  +---------+                       +---------+
|  dock01 |  |  dock02 |  [docker_hosts]       |  mon01  |  [monitoring]
|10.0.4.11|  |10.0.4.12|  compose stacks:      |10.0.5.11|  Prometheus + Grafana
+---------+  +---------+  acme-shop, registry  +---------+  + Alertmanager (compose)
                          + MinIO                            node_exporter on ALL hosts
```

Group tree: `acme` > `frontend` (`lb`, `web`), `backend` (`db`, `cache`),
`docker_hosts`, `monitoring`.

## Playbooks

| Playbook             | Hosts                  | Highlights                                            |
|----------------------|------------------------|-------------------------------------------------------|
| `site.yml`           | everything             | `import_playbook` orchestration of all stages          |
| `provision-users.yml`| acme                   | users/groups/SSH keys/sudo via `import_role`           |
| `hardening.yml`      | acme                   | `any_errors_fatal`, ufw/firewalld, fail2ban, CIS tags  |
| `databases.yml`      | db, cache              | PostgreSQL 16 + replication slots, Redis               |
| `webservers.yml`     | web                    | nginx vhosts, app deploy with rollback, play handlers  |
| `loadbalancers.yml`  | lb                     | haproxy.cfg from inventory, keepalived VIP, `run_once` |
| `docker-stack.yml`   | docker_hosts           | `strategy: free`, compose v2 stacks                    |
| `monitoring.yml`     | acme + monitoring      | node_exporter systemd unit everywhere, Prom/Grafana    |
| `backup.yml`         | db, cache, docker_hosts| systemd service + timer, cron fallback                 |
| `rolling-update.yml` | web                    | `serial: 1`, LB drain via `delegate_to`, health gates  |

## Roles

`common`, `users`, `security`, `docker`, `docker_apps`, `nginx`, `haproxy`,
`postgresql`, `redis`, `monitoring`, `backup`, `app_deploy` — each with
defaults, handlers where relevant, `meta/main.yml` galaxy metadata and role
dependencies (`docker_apps` -> `docker`, `monitoring` -> `docker`,
`app_deploy` -> `common`).

## Running with Pine

1. Add this directory as a **Project** in Pine (SCM path `examples/demo-infra`).
2. Create an **Inventory** from `inventories/production/hosts.ini`
   (or `inventories/staging/hosts.yml` for staging).
3. Install collections: `ansible-galaxy collection install -r requirements.yml`.
4. Create **Job Templates** for the playbooks above; useful starters:

```bash
# full converge, production
ansible-playbook site.yml

# only the web tier, staging inventory
ansible-playbook -i inventories/staging/hosts.yml webservers.yml

# zero-downtime deploy, one node at a time
ansible-playbook rolling-update.yml -e app_git_version=v2.5.0

# tag-scoped runs
ansible-playbook hardening.yml --tags firewall
ansible-playbook docker-stack.yml --tags compose
```

## Secrets

`vars/secrets.yml` contains placeholder values only. In a real deployment,
encrypt it with `ansible-vault encrypt vars/secrets.yml` and attach the vault
credential to the Pine job template.
