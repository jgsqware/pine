# Pine — Backlog des tâches restantes

> **Pourquoi ce fichier et pas TARS ?** Les écritures TARS sont bloquées côté
> backend (`403 "Only superusers can perform this action"` sur `POST /api/tasks`,
> régression de permissions apparue en cours de journée). Ce fichier contient le
> même backlog au format TARS, ordonné topologiquement — chaque bloc est
> ré-importable tel quel dès que les droits sont rétablis (`mcp__tars-tasks__task_add`
> avec `subject = "[id] titre"`, le corps en `description`, `depends_on` en tags `dep:*`).

**Source de vérité** : `docs/audit/AUDIT-Pine-2026-07.md` (audit multi-agents,
citations `fichier:ligne`) recoupé avec `ROADMAP.md` et l'historique git.

**Déjà fait cette session (exclu du backlog)** : Sprint 0 sécurité (auth/bind/CSRF/
git-allowlist/redaction/perms), hygiene smells + `pine hygiene`, 4 fixes UX workflow,
Re-run vars, Sprint 1 robustesse (flock/worker-pool/réconciliation), Sprint 2 partiel
(gzip + index jobs mémoire + pagination + `/stats` sans I/O).

Priorités : `crit` > `high` > `med` > `low`. L'ordre des sections **est** l'ordre
d'exécution (aucune tâche ne référence une tâche postérieure).

---

## Layer 0 — racines (aucune dépendance)

### `fix-precedence-vars-files` — Corriger la précédence vars_files vs play vars
- **depends_on** : []
- **priority** : high
- **contexte** : Les deux moteurs inversent la précédence Ansible — `play vars` écrase `vars_files` alors qu'Ansible fait l'inverse (play vars niveau 12 < vars_files niveau 14). Cohérents entre eux mais tous deux faux, non testés. Audit §3.2.
- **fichiers** : `internal/plan/resolve.go` (~L158-163, ordre `vars_file` puis `play_vars`, `add()` = dernière écriture gagne) ; `internal/plan/vars.go` (`effective()` ~L163-167) ; `internal/plan/resolve_test.go`.
- **étapes** :
  1. Dans `resolve.go`, ordre : `play.Vars` → `vars_prompt` → `vars_files` (vars_files gagne).
  2. Idem dans `vars.go/effective()` (play.Vars avant playFileVars).
  3. Test clé-définie-dans-les-deux → valeur du vars_files gagne.
- **acceptance** : test rouge→vert ; `go test ./internal/plan/` vert.
- **out_of_scope** : ne pas toucher role defaults/vars ni group/host vars ; pas de changement d'API.

### `fix-roleref-exact` — Détecter les rôles par RoleRef exact
- **depends_on** : []
- **priority** : high
- **contexte** : `hygiene`/`impact` détectent l'usage d'un rôle via `strings.Contains(t.Args, role.Name)` (sous-chaîne) → `db` matché par `mariadb`, `name` toujours matché. Faux négatifs unused_roles, blast radius surestimé. `model.Task.RoleRef` (exact) existe déjà. Audit §3.4.
- **fichiers** : `internal/plan/hygiene.go` (~L77 `markTask`) ; `internal/plan/impact.go` (~L294-299) ; `internal/plan/insights_test.go`.
- **étapes** :
  1. Remplacer le `Contains` par `t.RoleRef == role.Name` (hygiene + impact).
  2. Test : rôle `db` non utilisé + playbook contenant `mariadb` → `db` reste unused.
- **acceptance** : `go test ./internal/plan/` vert ; test anti-sous-chaîne dédié.
- **out_of_scope** : ne pas modifier la résolution transitive des deps ni `roleRef()` du scanner.

### `fix-host-patterns` — Gérer (ou marquer unknown) les patterns d'hôte `!` et `&`
- **depends_on** : []
- **priority** : high
- **contexte** : `MatchHosts` ignore `!` (exclusion) et `&` (intersection) : `webservers:!staging` renvoie tous les webservers, et le plan leur donne des verdicts fermes sans incertitude — contredit « honest engine ». Audit §3.5.
- **fichiers** : `internal/scanner/match.go` (~L26-54 `MatchHosts`) ; `internal/plan/plan.go` (~L273) ; `internal/scanner/match_test.go` (créer).
- **étapes** :
  1. Implémenter exclusion `!` et intersection `&` ; sinon exposer les hôtes « incertains ».
  2. Côté plan, marquer `unknown` les hôtes à appartenance incertaine.
  3. Tests `webservers:!staging`, `webservers:&prod`, combinés.
- **acceptance** : `go test ./internal/scanner/ ./internal/plan/` vert ; staging exclu ; aucun verdict ferme sur hôte exclu.
- **out_of_scope** : pas de regex `~` ni ranges `[0:5]`.

### `fix-cmpeq-typed` — Comparaison typée dans cmpEq
- **depends_on** : []
- **priority** : med
- **contexte** : `cmpEq` compare via `fmt.Sprintf("%v")` → `true == "true"` et `1 == "1"` vrais (faux en Jinja). Verdicts potentiellement erronés, silencieux. Audit §3.6a.
- **fichiers** : `internal/scanner/eval.go` (~L580-588 `cmpEq`, `boolTest` ~L378-399) ; test tri-state existant.
- **étapes** :
  1. Numérique via `toFloat` des deux côtés ; sinon bool/bool et string/string strict.
  2. Vérifier impact sur `in` et `is true/false`.
  3. Tests `1=="1"`→false, `true=="true"`→false, `1==1`→true.
- **acceptance** : `go test ./internal/scanner/` vert ; pas de régression tri-state.
- **out_of_scope** : ne pas toucher Kleene ni la propagation des vars manquantes.

### `sec-confine-playbook-arg` — Confiner l'argument playbook (préfixe `--`)
- **depends_on** : []
- **priority** : high
- **contexte** : `job.Playbook` passé en positionnel à `ansible-playbook` ; un chemin à tiret serait lu comme option. Confiner au workdir + couper l'ambiguïté. Audit §4.4.
- **fichiers** : `internal/runner/jobs.go` (`runAnsible`, argv ~L300-340) ; `internal/runner/*_test.go`. Réutiliser `withinRoot`/`EvalSymlinks` de `internal/server/server.go`.
- **étapes** :
  1. Valider playbook/inventaire relatifs sans `..`, confinés sous le workdir.
  2. Insérer `--` avant l'argument positionnel du playbook.
  3. Rejeter (erreur job claire) un playbook hors workdir.
- **acceptance** : `go test ./internal/runner/` vert ; `../evil.yml` / `-e...` neutralisés ; runs légitimes OK (simulé + réel).
- **out_of_scope** : ne pas re-designer vault/extra-vars (déjà temp 0600).

### `sec-cap-sse-log-memory` — Borner la mémoire du stream SSE de log
- **depends_on** : []
- **priority** : high
- **contexte** : `jobEvents` lit tout le log en mémoire sans limite → DoS mémoire sur job verbeux. Audit §4.4.
- **fichiers** : `internal/server/server.go` (`jobEvents` ~L619-644) ; `internal/runner/jobs.go` (`Subscribe`).
- **étapes** :
  1. Plafonner le replay initial (tail des N dernières Ko/lignes) ou streamer le fichier sans tout charger.
  2. Garder/expliquer le plafond de buffer par abonné.
  3. Test lecture bornée sur gros log.
- **acceptance** : `go test ./internal/server/` vert ; replay borné ; live-stream OK (SSE toujours non gzippé).
- **out_of_scope** : pas de websocket ; ne pas changer le format d'événements.

### `job-error-field` — Ajouter Job.Error et le renseigner
- **depends_on** : []
- **priority** : med
- **contexte** : `model.Job` n'a pas de champ d'erreur ; un échec (disque, exécution) n'est pas remonté au client. Audit §2 (A4/A6).
- **fichiers** : `internal/model/model.go` (struct `Job`) ; `internal/runner/jobs.go` (`execute`, poser `job.Error` sur échec) ; `web/app.js` (afficher `job.error` sur la page job) ; `internal/runner/ops_test.go`.
- **étapes** :
  1. Ajouter `Error string json:"error,omitempty"` à `Job`.
  2. Le renseigner dans `execute()` en cas d'échec (message court).
  3. L'afficher dans la page job web.
- **acceptance** : `go test ./...` vert + `node --check web/app.js` ; un job échoué expose un message via l'API.
- **out_of_scope** : ne pas refondre la remontée d'erreurs du store (voir `surface-store-write-errors`).

### `scan-cache-immutable` — Ne plus partager le pointeur de scan caché
- **depends_on** : []
- **priority** : med
- **contexte** : `Manager.Scan()` retourne le pointeur du `ScanResult` caché (partagé, mutable, invalidé seulement par `Forget`). Un consommateur qui mute le résultat corrompt le cache. Audit §2 (A6).
- **fichiers** : `internal/runner/manager.go` (`Scan` ~L124-144, `rescan`) ; `internal/runner/*_test.go`.
- **étapes** :
  1. Retourner une copie (deep ou shallow-safe) du `ScanResult`, ou documenter/imposer l'immutabilité côté appelants.
  2. (option) TTL de cache.
- **acceptance** : `go test ./...` vert ; muter le retour de `Scan()` n'altère pas un appel suivant.
- **out_of_scope** : ne pas ajouter la persistance disque du scan (voir `perf-persist-scan`).

### `perf-parallel-scan` — Paralléliser le scan (phases + parsing par item)
- **depends_on** : []
- **priority** : high
- **contexte** : `Scan()` lance 3 parcours séquentiels puis parse chaque fichier via yaml.v3 en série mono-thread ; sur debops (240 playbooks / 203 rôles) c'est plusieurs secondes bloquantes. Audit §5-P1.
- **fichiers** : `internal/scanner/scanner.go` (`Scan` ~L18-30) ; `internal/scanner/role.go` (`scanRoles` ~L18-49, boucle qui append puis `sort.Slice` par Name) ; `internal/scanner/playbook.go` (`scanPlaybooks` ~L61-95, append puis `sort.Slice` par Path).
- **étapes** :
  1. Lancer `scanPlaybooks`/`scanRoles`/`scanInventories` concurremment (3 goroutines, résultats indépendants).
  2. Dans `scanRoles`/`scanPlaybooks`, parser chaque rôle/playbook dans une goroutine bornée (semaphore `chan struct{}` taille GOMAXPROCS, stdlib, pas de dépendance), collecter puis `sort` final (déterminisme préservé).
  3. Attention aux `seen` maps partagées → dédupliquer AVANT le fan-out (calcul de la liste unique en séquentiel, parse en parallèle).
- **acceptance** : `go test ./internal/scanner/` vert (ordre identique) ; scan debops nettement plus rapide (mesure avant/après notée).
- **out_of_scope** : pas de cache incrémental ici (voir `perf-incremental-scan-cache`).

### `perf-listrel-walkdir` — Remplacer filepath.Walk par WalkDir dans listRel
- **depends_on** : []
- **priority** : low
- **contexte** : `listRel` (templates/ et files/ des rôles) utilise `filepath.Walk` (lstat par entrée), plus lent que `WalkDir` ; ×203 rôles = surcoût gratuit. Audit §5-P12.
- **fichiers** : `internal/scanner/scanner.go` (`listRel` ~L85-97).
- **étapes** :
  1. Remplacer par `filepath.WalkDir` (DirEntry, sans lstat superflu).
- **acceptance** : `go test ./internal/scanner/` vert ; sortie identique.
- **out_of_scope** : aucune autre optimisation de scan.

### `pipelineruns-per-file` — Un fichier par pipeline-run + purge
- **depends_on** : []
- **priority** : med
- **contexte** : `pipelineruns.json` est réécrit en entier à chaque run (O(N) par écriture, O(N²) sur la durée), jamais purgé ; `GetPipelineRun` charge tout pour un seul run. Audit §2-A2 / §5-P5.
- **fichiers** : `internal/store/collections.go` (`SavePipelineRun` ~L297-313, `GetPipelineRun` ~L287-293) ; `internal/store/store_test.go`.
- **étapes** :
  1. Stocker chaque run sous `pipelineruns/<id>.json` (comme les jobs), écriture O(1), lecture par ID directe.
  2. Purge/plafond de l'historique (N derniers par pipeline).
  3. Migration : charger l'ancien `pipelineruns.json` s'il existe.
- **acceptance** : test CRUD + purge vert ; plus de réécriture globale par run.
- **out_of_scope** : ne pas toucher schedules/pipelines (peu nombreux) — voir `perf-tickschedules-flush`.

### `store-interface` — Extraire une interface Store + testabilité
- **depends_on** : []
- **priority** : med
- **contexte** : Le store concret est utilisé partout, aucune interface, upserts dupliqués, peu testable. Prérequis pour RBAC (user store) et pour mocker en test. Audit §2-A6.
- **fichiers** : `internal/store/store.go` (+ `collections.go`) : définir une interface `Store` exposant les méthodes publiques ; adapter `internal/runner/manager.go` et `internal/server/server.go` à l'interface.
- **étapes** :
  1. Déclarer `type Store interface { … }` couvrant repos/jobs/facts/services/schedules/pipelines.
  2. Renommer l'implémentation concrète (`type fileStore struct`), `Open` retourne l'interface.
  3. Adapter les usages ; factoriser les upserts dupliqués.
- **acceptance** : `go build ./... && go test ./...` vert ; le manager/serveur dépendent de l'interface.
- **out_of_scope** : ne pas changer le format de persistance JSON.

### `perf-slim-scan-endpoint` — Endpoint /scan « slim » + détail à la demande
- **depends_on** : []
- **priority** : high
- **contexte** : `/scan` sérialise tout le `ScanResult` (arbres de tâches complets, multi-Mo) à chaque ouverture de repo ; l'UI n'affiche qu'un playbook à la fois. Audit §5-P7.
- **fichiers** : `internal/server/server.go` (`scanRepo` ~L359-380 ; ajouter `GET /repos/{id}/playbooks/{path}` détail) ; `web/app.js` (consommation du scan, ~L486 et le browser de playbooks) ; `README.md` table API.
- **étapes** :
  1. Ajouter un `/scan?slim=1` (ou nouvel endpoint) renvoyant métadonnées + compteurs (listes playbooks/rôles/inventaires) sans arbres de tâches.
  2. Endpoint détail par playbook/rôle chargé à la demande.
  3. Adapter le front à charger le détail au clic.
  4. Documenter dans la table API du README.
- **acceptance** : `go test ./internal/server/` + `node --check web/app.js` verts ; ouverture d'un gros repo transfère un payload réduit.
- **out_of_scope** : pas de virtualisation DOM (voir `web-virtualize-lists`).

### `web-a11y-keyboard-controls` — Rendre les contrôles span/div accessibles au clavier
- **depends_on** : []
- **priority** : med
- **contexte** : De nombreux contrôles cliquables sont des `span`/`div` avec `onclick` sans `tabindex`/`role`/handler clavier (onglets, en-têtes repliables, tokens de variable). Inaccessibles clavier/lecteur d'écran. Audit §6.
- **fichiers** : `web/app.js` (~L1156, 2213, 1328, 1336 ; helper `el()`).
- **étapes** :
  1. Convertir ces contrôles en `<button>` (le helper `el()` le permet), ou ajouter `role="button"`, `tabindex="0"` et un handler `keydown` Enter/Espace mutualisé.
- **acceptance** : `node --check web/app.js` ; les contrôles sont focusables et activables au clavier (vérif manuelle/Playwright ultérieure).
- **out_of_scope** : ne pas refondre le CSS (voir `web-a11y-focus`).

### `web-a11y-focus` — Focus-trap des modales + focus-visible
- **depends_on** : []
- **priority** : low
- **contexte** : `openModal` focus le 1er champ mais sans piège de focus (Tab sort vers le fond) ni restauration à la fermeture ; `:focus-visible` absent (peu visible sur fond sombre). Audit §6.
- **fichiers** : `web/app.js` (`openModal`/`closeModal` ~L215-249) ; `web/style.css` (~L190-197).
- **étapes** :
  1. Boucler Tab/Shift+Tab sur les éléments focusables de la modale ; mémoriser/restaurer `document.activeElement`.
  2. Règle globale `:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px }` sur boutons/liens/[role=button].
- **acceptance** : `node --check web/app.js` ; Tab reste dans la modale ; anneau de focus visible.
- **out_of_scope** : ne pas toucher la logique métier des modales.

### `feat-github-action-plan-impact` — GitHub Action pine plan/impact (coin CI)
- **depends_on** : []
- **priority** : med
- **contexte** : Le différenciateur GTM est la CI : `pine plan` (exit 3 sur unknowns) et `pine impact` (exit 3 sur hôtes impactés) sont déjà CI-friendly ; packager une Action officielle. Audit §7 / ROADMAP GTM.
- **fichiers** : `.github/actions/pine-plan/action.yml` (+ éventuel wrapper) ; `docs/` exemple de workflow ; `README.md` section CI. Réutiliser le binaire `pine` (build `./cmd/pine`).
- **étapes** :
  1. Créer une composite/Docker action installant `pine` et lançant `pine plan`/`pine impact` sur le repo Ansible.
  2. Propager le code de sortie (3) pour gater la PR ; option post-comment (facultatif).
  3. Exemple `.github/workflows/*.yml` documenté + section README.
- **acceptance** : l'action tourne sur le repo de démo (`examples/demo-infra`) et échoue quand un plan a des unknowns ou un diff impacte des hôtes.
- **out_of_scope** : pas de « Pine Cloud » hébergé ; pas de commentaire PR riche (v1 minimal).

---

## Layer 1 — dépend de Layer 0

### `fix-plan-role-vars-main` — Inclure role vars/main.yml dans le moteur de plan
- **depends_on** : [`fix-precedence-vars-files`]  *(même fichier vars.go/plan.go → sérialisé)*
- **priority** : high
- **contexte** : `varResolver.effective()` ne merge que `r.Defaults`, jamais `r.Vars` (`vars/main.yml`) → une `when:`/`{{ }}` référençant une var de role vars/main.yml donne un faux `unknown`. Incohérent avec resolve.go/lineage.go. Audit §3.3.
- **fichiers** : `internal/plan/plan.go` (~L308-313 collecte `roleDefaults`) ; `internal/plan/vars.go` (`effective()` ~L98-186) ; `internal/plan/plan_test.go`.
- **étapes** :
  1. Passer aussi `r.Vars` au resolver, merger après play vars/vars_files et avant les userVars (précédence 15).
  2. Test : var définie en role `vars/main.yml` référencée dans un `when:` → plus de faux `unknown`.
- **acceptance** : `go test ./internal/plan/` vert ; alignement avec resolve.go.
- **out_of_scope** : ne pas changer la précédence corrigée par `fix-precedence-vars-files`.

### `fix-unused-vars-multidef` — Corriger le faux négatif unused_vars (définitions multiples)
- **depends_on** : [`fix-roleref-exact`]  *(même fichier hygiene.go → sérialisé)*
- **priority** : low
- **contexte** : `usedOutsideDefinition` retourne `counts[key] > defs[key]` ; une var définie en group_vars ET host_vars (2 defs) jamais utilisée a counts==defs==2 → non signalée. Audit §3.6b.
- **fichiers** : `internal/plan/hygiene.go` (~L434-436 `usedOutsideDefinition`, index `textIndex`) ; `internal/plan/insights_test.go`.
- **étapes** :
  1. Compter les usages hors lignes de définition indépendamment du nombre de définitions (ex. comparer à `defs[key]` réel par occurrence de définition distincte).
  2. Test : var définie 2× jamais utilisée → listée unused.
- **acceptance** : `go test ./internal/plan/` vert ; cas multi-def couvert.
- **out_of_scope** : ne pas modifier la détection de secrets (déjà élargie).

### `perf-incremental-scan-cache` — Cache incrémental de scan par (path, mtime, size)
- **depends_on** : [`perf-parallel-scan`]
- **priority** : high
- **contexte** : Rien n'est mis en cache entre deux syncs au niveau fichier ; un re-sync re-parse tout. Audit §5-P1.
- **fichiers** : `internal/scanner/` (parseurs de fichiers rôle/playbook) ; `internal/runner/manager.go` (`rescan`).
- **étapes** :
  1. Mémoïser le parse par `(path, mtime, size)` ; ne re-parser que les fichiers modifiés entre deux syncs.
  2. Invalidation propre quand un fichier disparaît.
- **acceptance** : `go test ./internal/scanner/` vert ; un re-sync sans changement ne re-parse pas les fichiers (mesure/compteur).
- **out_of_scope** : pas de persistance disque du cache (voir `perf-persist-scan`).

### `perf-singleflight-scan` — Dédupliquer les scans concurrents (singleflight)
- **depends_on** : [`scan-cache-immutable`]
- **priority** : med
- **contexte** : Sur cache miss, `Manager.Scan` libère le mutex puis scanne dans la goroutine HTTP ; N requêtes concurrentes lancent N scans complets (thundering herd). Audit §5-P3.
- **fichiers** : `internal/runner/manager.go` (`Scan` ~L124-144).
- **étapes** :
  1. Implémenter un singleflight minimal (map `repoID → *sync.Once`/chan) pour qu'un seul scan tourne par repo, les autres attendent le résultat.
- **acceptance** : `go test ./internal/runner/` vert ; N appels concurrents sur un repo non caché déclenchent un seul scan (compteur).
- **out_of_scope** : pas de warm-up au boot (fait par `perf-persist-scan`).

### `perf-memoize-import-tasks` — Mémoïser le parse des import_tasks
- **depends_on** : [`perf-parallel-scan`]  *(même fichier playbook.go → sérialisé)*
- **priority** : low
- **contexte** : `resolveImportTasks` re-parse le fichier cible à chaque occurrence et recopie la map `seen` à chaque descente. Audit §5-P11.
- **fichiers** : `internal/scanner/playbook.go` (`resolveImportTasks` ~L439-474).
- **étapes** :
  1. Cache de parse par chemin partagé le temps d'un scan.
  2. Détection de cycle via pile avec dépilement (sans recopier la map).
- **acceptance** : `go test ./internal/scanner/` vert ; sortie identique.
- **out_of_scope** : ne pas changer la sémantique d'inlining.

### `surface-store-write-errors` — Remonter les erreurs d'écriture du store
- **depends_on** : [`job-error-field`]
- **priority** : med
- **contexte** : ~des dizaines d'erreurs ignorées (`_ = Save…`) ; le save terminal d'un job ignore l'erreur → un échec disque laisse un état incohérent. Audit §2-A4.
- **fichiers** : `internal/runner/jobs.go` (saves dans `execute`) ; `internal/runner/*.go` autres `_ = m.Store.Save…` ; `cmd/pine/*` selon besoin.
- **étapes** :
  1. Propager/loguer les erreurs de `SaveJob`/`UpdateRepo` critiques (au minimum log + `job.Error`).
  2. Ne pas laisser un job « running » si le save final échoue (fallback).
- **acceptance** : `go test ./...` vert ; une erreur d'écriture est visible (log/`job.Error`), pas silencieuse.
- **out_of_scope** : ne pas tout convertir — cibler les chemins critiques (jobs, repos).

### `perf-sse-status-push` — Pousser le statut de job au lieu de le sonder toutes les 2s
- **depends_on** : [`sec-cap-sse-log-memory`, `job-error-field`]
- **priority** : med
- **contexte** : Le handler SSE relit `GetJob` toutes les 2s par client connecté (I/O + contention) alors que l'état ne change qu'à quelques transitions. Audit §5-P8.
- **fichiers** : `internal/server/server.go` (`jobEvents` ticker ~L619-644) ; `internal/runner/jobs.go` (publier les transitions de statut sur le même canal que les lignes).
- **étapes** :
  1. Émettre un événement `status` depuis `run` uniquement aux transitions.
  2. Servir le dernier `model.Job` depuis la mémoire (sans I/O).
- **acceptance** : `go test ./internal/server/` vert ; plus de lecture disque périodique par client ; le front reçoit toujours les transitions.
- **out_of_scope** : ne pas changer le contrat d'événements côté front.

### `perf-tickschedules-flush` — Un seul flush groupé par tick de scheduler
- **depends_on** : [`pipelineruns-per-file`]  *(même fichier collections.go → sérialisé)*
- **priority** : low
- **contexte** : `tickSchedules` (30s) relit + réécrit tout `schedules.json` pour presque chaque branche ; O(N²) si plusieurs schedules dus. Audit §5-P10.
- **fichiers** : `internal/runner/scheduler.go` (`tickSchedules` ~L146-198) ; `internal/store/collections.go` (`SaveSchedule`).
- **étapes** :
  1. Charger les schedules une fois, muter en mémoire, flush unique groupé en fin de tick.
  2. Espacer/mutualiser les `plan.Compute` gated.
- **acceptance** : `go test ./internal/runner/` vert ; un tick avec K schedules dus fait 1 écriture, pas K.
- **out_of_scope** : ne pas changer la sémantique de gating des schedules.

### `web-module-split` — Découper app.js en modules ES
- **depends_on** : [`web-a11y-keyboard-controls`, `web-a11y-focus`]  *(même fichier app.js → sérialisé)*
- **priority** : med
- **contexte** : `web/app.js` = 5700+ lignes, ~120 fonctions, état global dispersé, aucun test. Principal risque de maintenabilité. Audit §6.
- **fichiers** : `web/app.js` → `web/js/{router,api,dom,state,pages/*}.js` ; `web/index.html` (`<script type="module">`) ; `web/embed.go` (vérifier l'embed des nouveaux fichiers).
- **étapes** :
  1. Découper en modules ES (import/export) par domaine — servable sans build via `type=module` (pas de bundler, pas de CDN, conforme aux conventions).
  2. Adapter `index.html` et l'embed Go.
  3. `node --check` sur chaque module.
- **acceptance** : app fonctionne (mock/verif) ; `node --check` sur tous les modules ; aucun CDN/build introduit.
- **out_of_scope** : pas de framework (reste vanilla) ; pas de refonte visuelle.

### `web-virtualize-lists` — Paginer/virtualiser les grandes listes
- **depends_on** : [`perf-slim-scan-endpoint`]
- **priority** : med
- **contexte** : Le rendu vide+recrée le DOM par `innerHTML` sans virtualisation ; gros repo (centaines de playbooks/hôtes, matrice hôtes×tâches) fige l'onglet. Audit §5-P9.
- **fichiers** : `web/app.js` (rendus de listes playbooks/tâches/hôtes ; polling jobs ~L4664-4687).
- **étapes** :
  1. Pagination/filtre des listes + chargement du détail à la demande (s'appuie sur l'endpoint slim).
  2. Fenêtrage (virtualisation) des longues listes et de la matrice hôtes×tâches.
  3. Updates ciblées (patch de lignes) sur les listes à polling (conserver scroll/sélection).
- **acceptance** : ouverture d'un gros repo fluide (mesure), scroll/sélection préservés au polling.
- **out_of_scope** : ne pas réécrire tout le rendu — cibler les listes coûteuses.

---

## Layer 2 — dépend de Layer 1

### `fix-plan-estimate-redaction` — Rédiger le plan estimé (secrets vault déchiffrés)
- **depends_on** : [`fix-plan-role-vars-main`]  *(même fichier plan.go → sérialisé)*
- **priority** : high
- **contexte** : Quand un vault password est fourni, `decryptVaultVars` injecte le clair dans `tp.Name`/`tp.Args` et le `Result` de `Compute` n'est jamais rédacté (contrairement à lineage/resolve). Audit §3.6c / §4.4.
- **fichiers** : `internal/plan/plan.go` (~L347-355, 445-455) ; `internal/plan/vault_test.go`.
- **étapes** :
  1. Appliquer une redaction (réutiliser `IsSecretKey` + `isVaultValue`) sur les noms/args résolus du plan, ou masquer les valeurs interpolées issues d'une clé sensible.
  2. Test : un `{{ db_password }}` déchiffré n'apparaît pas en clair dans le plan renvoyé.
- **acceptance** : `go test ./internal/plan/` vert ; aucun secret déchiffré dans le `Result`.
- **out_of_scope** : ne pas empêcher le déchiffrement pour l'évaluation (rester interne).

### `perf-magic-groups-once` — Calculer la magic var `groups` une fois par play
- **depends_on** : [`fix-plan-role-vars-main`]  *(même fichier vars.go/plan.go → sérialisé)*
- **priority** : med
- **contexte** : `effective()` reconstruit `eff["groups"]` (tous groupes × tous hôtes) pour CHAQUE hôte → O(hôtes²) en temps et allocations sur gros inventaire. C'est host-indépendant. Audit §5-P4.
- **fichiers** : `internal/plan/vars.go` (~L133-141) ; `internal/plan/plan.go` (boucle par hôte ~L340-356).
- **étapes** :
  1. Calculer `allGroups`/liste des groupes une seule fois par play (ou à la construction du resolver) et injecter la même référence.
  2. (option) lazy : ne matérialiser `groups` que si une expression le référence.
- **acceptance** : `go test ./internal/plan/` vert ; complexité linéaire en hôtes (mesure sur grand inventaire).
- **out_of_scope** : ne pas changer la sémantique des groupes constructed.

### `extract-manager-plan` — Unifier la logique de plan (Manager.Plan partagé)
- **depends_on** : [`fix-plan-role-vars-main`, `scan-cache-immutable`]
- **priority** : med
- **contexte** : Le plan diverge selon le point d'entrée : `computePlan` (web) injecte TaskDurations/HostFacts/vault, mais `localEngine.Plan` et `cmdPlan` non → plans différents web/TUI/CLI. Audit §2-A5.
- **fichiers** : `internal/runner/manager.go` (nouveau `Manager.Plan`) ; `internal/server/server.go` (`computePlan` ~L486-488) ; `internal/tui/engine.go` (~L45-53) ; `cmd/pine/main.go` (`cmdPlan` ~L609).
- **étapes** :
  1. Extraire une méthode `Manager.Plan(req)` centralisant l'injection facts/durations/vault.
  2. Câbler web, TUI et CLI dessus.
- **acceptance** : `go test ./...` vert ; un même playbook/inventaire donne le même plan via web, TUI et CLI.
- **out_of_scope** : ne pas étendre la couverture de `tui.Engine` aux 40 endpoints.

### `perf-persist-scan` — Persister le scan sur disque + warm-up au boot
- **depends_on** : [`perf-singleflight-scan`]
- **priority** : med
- **contexte** : Le cache de scan est purement en mémoire, perdu à chaque redémarrage ; le 1er `/scan` après boot bloque le client. Audit §5-P3.
- **fichiers** : `internal/runner/manager.go` (Scan/rescan) ; `internal/store/` (fichier `scan.<repoID>.json`).
- **étapes** :
  1. Sérialiser le `ScanResult` sur disque, recharger au boot, invalider par un hash de sync.
  2. Réchauffer le cache en tâche de fond (goroutine par repo) au démarrage.
- **acceptance** : `go test ./...` vert ; le 1er `/scan` après reboot sert depuis le cache persisté ; invalidation correcte après sync.
- **out_of_scope** : ne pas dupliquer la logique du cache incrémental fichier (réutiliser).

### `web-playwright-smoke` — Tests de fumée Playwright des parcours clés
- **depends_on** : [`web-module-split`]
- **priority** : med
- **contexte** : Aucun test JS ; toute régression de rendu passe inaperçue. Playwright est dispo. Audit §6.
- **fichiers** : `web/tests/*.spec.js` (nouveau) ; `package.json` minimal (dev only) ou script mise ; `Makefile`/`mise.toml` cible de test.
- **étapes** :
  1. Lancer `pine serve --demo` et scénariser : ouvrir un playbook → Plan → Apply(confirm) → job log ; page Hygiene ; navigation sidebar.
  2. Assertions sur états clés (verdict bar, log live, section smells).
  3. Cible `make test-web` (ou mise) documentée.
- **acceptance** : les specs passent en local contre le démo ; documentées dans le README.
- **out_of_scope** : pas de CI GitHub ici (pourra réutiliser `feat-github-action-plan-impact`).

---

## Layer 3 — dépend de Layer 2

### `fix-serial-percent-list` — Gérer serial en pourcentage et en liste
- **depends_on** : [`fix-plan-estimate-redaction`]  *(même fichier plan.go → sérialisé)*
- **priority** : low
- **contexte** : `atoiSafe(play.Serial)` renvoie 0 pour `30%` ou `[1,5,10]` → un seul batch, silencieusement. Audit §3.6d.
- **fichiers** : `internal/plan/plan.go` (~L299, 605-614) ; `internal/plan/plan_test.go`.
- **étapes** :
  1. Gérer le suffixe `%` (pourcentage du nombre d'hôtes) et la forme liste de `serial`.
  2. Tests `serial: 30%`, `serial: [1,5,10]`.
- **acceptance** : `go test ./internal/plan/` vert ; batches corrects pour les 3 formes.
- **out_of_scope** : ne pas modifier l'ordonnancement d'exécution réel (affichage/plan seulement).

### `perf-parse-when-once` — Parser l'expression `when` une fois par tâche
- **depends_on** : [`fix-cmpeq-typed`, `fix-serial-percent-list`]  *(eval.go/plan.go → sérialisé)*
- **priority** : med
- **contexte** : `EvalCondition(when, eff[h])` re-tokenise et re-parse la même chaîne `when` pour chaque hôte alors que l'AST est host-indépendant. Audit §5-P6.
- **fichiers** : `internal/plan/plan.go` (`task()` boucle par hôte ~L480-500) ; `internal/scanner/eval.go` (séparer parse et eval : exposer un parse → AST/closure, puis eval).
- **étapes** :
  1. Ajouter une API `ParseCondition(s) (evaluator, error)` dans eval.go ; parser 1× par tâche.
  2. Évaluer H fois avec `eff[h]` ; mémoïser le parse par chaîne.
- **acceptance** : `go test ./internal/scanner/ ./internal/plan/` vert ; parse effectué 1×/tâche (compteur), résultats identiques.
- **out_of_scope** : ne pas changer la grammaire des expressions.

---

## Layer 4 — grandes features (dépendances de fondation)

### `feat-rbac-sso-audit` — RBAC minimal + audit log (fondation open-core)
- **depends_on** : [`store-interface`]
- **priority** : med
- **contexte** : Bloque toute vente entreprise (audit §7 gaps) ; l'auth par token existe (Sprint 0) mais pas de rôles par utilisateur ni de journal d'audit. À découper si trop gros.
- **fichiers** : `internal/store/` (user store via l'interface) ; `internal/server/server.go` (middleware d'autorisation par rôle au-dessus de `secure`) ; `internal/model/model.go` (User/Role) ; `web/` (écran login/gestion users) ; `README.md`.
- **étapes** :
  1. Modèle User + Role (viewer/operator/admin), stockés via l'interface Store.
  2. Middleware d'autorisation par endpoint (lecture vs run vs admin) au-dessus de l'auth token existante.
  3. Journal d'audit append-only (qui a lancé quel job/plan/repo).
  4. (option SSO OIDC — tâche séparée si nécessaire).
- **acceptance** : `go test ./...` vert ; un rôle viewer ne peut pas `POST /api/jobs` ; les actions sont journalisées.
- **out_of_scope** : SSO/OIDC complet (v2) ; UI de gestion avancée.

### `feat-web-ssh-console` — Console SSH par hôte dans le navigateur (roadmap #10)
- **depends_on** : [`web-module-split`]
- **priority** : med
- **contexte** : ROADMAP item #10 (planned, delibérément dernier — besoin de vraies cibles SSH). La TUI a déjà `s`. xterm.js + websocket, vendored (pas de CDN). Roadmap.
- **fichiers** : `internal/server/server.go` (endpoint websocket `/api/hosts/{…}/ssh`) ; `web/js/` (module console, xterm.js vendored dans `web/vendor/`) ; `web/embed.go`.
- **étapes** :
  1. Endpoint websocket côté serveur reliant un PTY SSH vers l'hôte (auth token + garde CSRF/Origin réutilisées).
  2. Front : terminal xterm.js vendored (aucun CDN), branché sur le websocket.
  3. Valider contre de vraies cibles SSH.
- **acceptance** : ouvrir un terminal fonctionnel vers un hôte de test ; aucun CDN ajouté ; respecte l'auth.
- **out_of_scope** : pas d'enregistrement de session ; pas de partage multi-utilisateur.

---

## Layer 5 — docs / DoD

### `docs-capture-real-screenshots` — Capturer les screenshots réels (remplacer les mocks)
- **depends_on** : []  *(indépendant — nécessite juste un environnement où l'app peut binder)*
- **priority** : low
- **contexte** : Le CLAUDE.md exige des screenshots réels ; le sandbox actuel bloque le bind réseau, d'où des `*-mock.png` (hygiene, sidebar, apply). À recapturer en vrai quand l'app tourne. CLAUDE.md §6.
- **fichiers** : `docs/screenshots/*.png` (remplacer `hygiene-smells-mock.png` et ajouter les vues UX : sidebar, apply-confirm, job avec vars) ; `README.md` (retirer les mentions *(rendered mock)*).
- **étapes** :
  1. `pine serve --demo`, capturer via Playwright les vues : Hygiene (smells), sidebar groupée, dialogue Apply, page job (ligne vars).
  2. Remplacer les mocks, mettre à jour les légendes README.
- **acceptance** : screenshots réels dans `docs/screenshots/` ; plus de mention « rendered mock ».
- **out_of_scope** : ne pas fabriquer de captures (interdit) — uniquement des vraies.

---

## DAG des dépendances (ordre d'exécution)

```
Layer 0 (racines):
  fix-precedence-vars-files
  fix-roleref-exact
  fix-host-patterns
  fix-cmpeq-typed
  sec-confine-playbook-arg
  sec-cap-sse-log-memory
  job-error-field
  scan-cache-immutable
  perf-parallel-scan
  perf-listrel-walkdir
  pipelineruns-per-file
  store-interface
  perf-slim-scan-endpoint
  web-a11y-keyboard-controls
  web-a11y-focus
  feat-github-action-plan-impact

Layer 1:
  fix-plan-role-vars-main      ← fix-precedence-vars-files
  fix-unused-vars-multidef     ← fix-roleref-exact
  perf-incremental-scan-cache  ← perf-parallel-scan
  perf-singleflight-scan       ← scan-cache-immutable
  perf-memoize-import-tasks    ← perf-parallel-scan
  surface-store-write-errors   ← job-error-field
  perf-sse-status-push         ← sec-cap-sse-log-memory, job-error-field
  perf-tickschedules-flush     ← pipelineruns-per-file
  web-module-split             ← web-a11y-keyboard-controls, web-a11y-focus
  web-virtualize-lists         ← perf-slim-scan-endpoint

Layer 2:
  fix-plan-estimate-redaction  ← fix-plan-role-vars-main
  perf-magic-groups-once       ← fix-plan-role-vars-main
  extract-manager-plan         ← fix-plan-role-vars-main, scan-cache-immutable
  perf-persist-scan            ← perf-singleflight-scan
  web-playwright-smoke         ← web-module-split

Layer 3:
  fix-serial-percent-list      ← fix-plan-estimate-redaction
  perf-parse-when-once         ← fix-cmpeq-typed, fix-serial-percent-list

Layer 4:
  feat-rbac-sso-audit          ← store-interface
  feat-web-ssh-console         ← web-module-split

Layer 5:
  docs-capture-real-screenshots (indépendant)
```
