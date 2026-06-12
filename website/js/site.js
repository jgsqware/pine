/* Pine marketing site — self-contained, no dependencies. */
(function () {
  "use strict";

  var prefersReduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;

  /* ================= reveal on scroll ================= */
  var revealEls = document.querySelectorAll(".reveal");
  if ("IntersectionObserver" in window && !prefersReduced) {
    var ro = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) {
        if (e.isIntersecting) {
          e.target.classList.add("in");
          ro.unobserve(e.target);
        }
      });
    }, { threshold: 0.12, rootMargin: "0px 0px -40px 0px" });
    revealEls.forEach(function (el) { ro.observe(el); });
  } else {
    revealEls.forEach(function (el) { el.classList.add("in"); });
  }

  /* ================= tabs ================= */
  document.querySelectorAll("[data-tabs]").forEach(function (root) {
    var tabs = root.querySelectorAll(".tab");
    tabs.forEach(function (tab) {
      tab.addEventListener("click", function () {
        tabs.forEach(function (t) {
          t.classList.toggle("active", t === tab);
          t.setAttribute("aria-selected", t === tab ? "true" : "false");
        });
        root.querySelectorAll(".tabpane").forEach(function (p) {
          p.classList.toggle("active", p.id === tab.dataset.tab);
        });
      });
    });
  });

  /* ================= copy buttons ================= */
  document.querySelectorAll(".copy-btn").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var pre = btn.parentElement.querySelector("pre");
      if (!pre) return;
      var text = pre.textContent.replace(/^\$ /gm, "");
      var done = function () {
        btn.textContent = "Copied!";
        btn.classList.add("copied");
        setTimeout(function () {
          btn.textContent = "Copy";
          btn.classList.remove("copied");
        }, 1600);
      };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(done, done);
      } else { done(); }
    });
  });

  /* ============================================================
     Topology graph — tiny force layout over the Acme production
     inventory, rendered into #topo as SVG.
     ============================================================ */
  (function topology() {
    var svg = document.getElementById("topo");
    if (!svg) return;
    var NS = "http://www.w3.org/2000/svg";
    var W = 860, H = 540, CX = W / 2, CY = H / 2;

    // id, parent, type, radius, constructed?
    var spec = [
      ["acme", null, "group", 27],
      ["frontend", "acme", "group", 21],
      ["backend", "acme", "group", 21],
      ["docker_hosts", "acme", "group", 19, true],
      ["monitoring", "acme", "group", 19],
      ["lb", "frontend", "group", 16],
      ["web", "frontend", "group", 16],
      ["db", "backend", "group", 16],
      ["cache", "backend", "group", 16],
      ["lb01", "lb", "host", 9], ["lb02", "lb", "host", 9],
      ["web01", "web", "host", 9], ["web02", "web", "host", 9], ["web03", "web", "host", 9],
      ["db-primary", "db", "host", 9], ["db-replica", "db", "host", 9],
      ["redis01", "cache", "host", 9],
      ["dock01", "docker_hosts", "host", 9], ["dock02", "docker_hosts", "host", 9],
      ["mon01", "monitoring", "host", 9]
    ];

    var nodes = {}, list = [], links = [];
    spec.forEach(function (s, i) {
      // deterministic radial seed: depth ring + angle by index
      var depth = s[1] === null ? 0 : (s[1] === "acme" ? 1 : (s[3] > 10 ? 2 : 3));
      var ang = (i / spec.length) * Math.PI * 2 + depth * 0.7;
      var n = {
        id: s[0], parent: s[1], type: s[2], r: s[3], constructed: !!s[4],
        x: CX + Math.cos(ang) * (40 + depth * 95),
        y: CY + Math.sin(ang) * (30 + depth * 60),
        vx: 0, vy: 0
      };
      nodes[n.id] = n; list.push(n);
      if (s[1]) links.push({ a: s[1], b: s[0] });
    });
    nodes.acme.x = CX; nodes.acme.y = CY;

    // adjacency for hover highlighting (node + ancestors + descendants)
    var children = {};
    links.forEach(function (l) { (children[l.a] = children[l.a] || []).push(l.b); });
    function lineage(id) {
      var keep = {}, n;
      for (n = nodes[id]; n; n = n.parent ? nodes[n.parent] : null) keep[n.id] = true;
      (function down(x) {
        keep[x] = true;
        (children[x] || []).forEach(down);
      })(id);
      return keep;
    }

    // ---- build SVG ----
    var linkEls = links.map(function (l) {
      var el = document.createElementNS(NS, "line");
      el.setAttribute("class", "link");
      svg.appendChild(el);
      l.el = el;
      return el;
    });
    list.forEach(function (n) {
      var g = document.createElementNS(NS, "g");
      g.setAttribute("class", "node node-" + n.type + (n.constructed ? " node-constructed" : ""));
      var c = document.createElementNS(NS, "circle");
      c.setAttribute("r", n.r);
      var t = document.createElementNS(NS, "text");
      t.textContent = n.id;
      if (n.type === "group") {
        t.setAttribute("text-anchor", "middle");
        t.setAttribute("dy", n.r + 14);
      } else {
        t.setAttribute("dx", n.r + 5);
        t.setAttribute("dy", 4);
      }
      g.appendChild(c); g.appendChild(t);
      svg.appendChild(g);
      n.el = g;

      g.addEventListener("mouseenter", function () { focus(n.id); });
      g.addEventListener("mouseleave", unfocus);
    });

    var readout = document.getElementById("topo-readout");
    function describe(n) {
      if (n.type === "host") return n.id + " — host · group: " + n.parent;
      var kids = children[n.id] || [];
      var hostCount = 0;
      (function count(x) {
        if (nodes[x].type === "host") hostCount++;
        (children[x] || []).forEach(count);
      })(n.id);
      return n.id + " — " + (n.constructed ? "constructed group" : "group") +
        " · " + hostCount + " host" + (hostCount === 1 ? "" : "s") +
        (kids.length ? " · children: " + kids.join(", ") : "") +
        (n.constructed ? " · built from services var" : "");
    }
    function focus(id) {
      var keep = lineage(id);
      svg.classList.add("focus");
      list.forEach(function (n) { n.el.classList.toggle("hl", !!keep[n.id]); });
      links.forEach(function (l) { l.el.classList.toggle("hl", keep[l.a] && keep[l.b]); });
      if (readout) readout.textContent = describe(nodes[id]);
    }
    function unfocus() {
      svg.classList.remove("focus");
      list.forEach(function (n) { n.el.classList.remove("hl"); });
      links.forEach(function (l) { l.el.classList.remove("hl"); });
      if (readout) readout.textContent = "acme — 11 hosts, 8 child groups";
    }

    // ---- force simulation (~50 lines) ----
    function tick() {
      var i, j, a, b, dx, dy, d2, d, f;
      // pairwise repulsion
      for (i = 0; i < list.length; i++) {
        for (j = i + 1; j < list.length; j++) {
          a = list[i]; b = list[j];
          dx = b.x - a.x; dy = b.y - a.y;
          d2 = dx * dx + dy * dy + 0.01;
          if (d2 > 90000) continue;
          f = 2600 / d2;
          d = Math.sqrt(d2);
          dx /= d; dy /= d;
          a.vx -= dx * f; a.vy -= dy * f;
          b.vx += dx * f; b.vy += dy * f;
        }
      }
      // springs along links
      links.forEach(function (l) {
        a = nodes[l.a]; b = nodes[l.b];
        dx = b.x - a.x; dy = b.y - a.y;
        d = Math.sqrt(dx * dx + dy * dy) + 0.01;
        var rest = a.r + b.r + (b.type === "host" ? 46 : 78);
        f = (d - rest) * 0.045;
        dx /= d; dy /= d;
        a.vx += dx * f; a.vy += dy * f;
        b.vx -= dx * f; b.vy -= dy * f;
      });
      // gentle pull to center + integrate
      list.forEach(function (n) {
        n.vx += (CX - n.x) * 0.004;
        n.vy += (CY - n.y) * 0.006;
        n.vx *= 0.82; n.vy *= 0.82;
        n.x += n.vx; n.y += n.vy;
        var m = n.r + 16;
        n.x = Math.max(m, Math.min(W - m - 40, n.x));
        n.y = Math.max(m, Math.min(H - m - 6, n.y));
      });
      nodes.acme.x += (CX - nodes.acme.x) * 0.2;
      nodes.acme.y += (CY - nodes.acme.y) * 0.2;
    }
    function render() {
      links.forEach(function (l) {
        l.el.setAttribute("x1", nodes[l.a].x); l.el.setAttribute("y1", nodes[l.a].y);
        l.el.setAttribute("x2", nodes[l.b].x); l.el.setAttribute("y2", nodes[l.b].y);
      });
      list.forEach(function (n) {
        n.el.setAttribute("transform", "translate(" + n.x.toFixed(1) + "," + n.y.toFixed(1) + ")");
      });
    }

    // settle most of the way instantly, then animate the last bit when visible
    for (var k = 0; k < 160; k++) tick();
    render();
    if (!prefersReduced && "IntersectionObserver" in window) {
      var ticks = 110;
      var io = new IntersectionObserver(function (entries) {
        if (!entries[0].isIntersecting) return;
        io.disconnect();
        (function frame() {
          tick(); render();
          if (--ticks > 0) requestAnimationFrame(frame);
        })();
      }, { threshold: 0.25 });
      io.observe(svg);
    } else {
      for (k = 0; k < 110; k++) tick();
      render();
    }
  })();

  /* ============================================================
     Job output — typewriter-streamed ansible-playbook run
     ============================================================ */
  (function jobRun() {
    var out = document.getElementById("job-out");
    if (!out) return;
    var cursor = document.getElementById("job-cursor");
    var term = document.getElementById("job-term");
    var statusEl = document.getElementById("job-status");
    var replay = document.getElementById("job-replay");

    var L = function (cls, text, pause) { return { c: cls, t: text, p: pause || 0 }; };
    function banner(s) {
      var line = s + " ";
      while (line.length < 64) line += "*";
      return line;
    }

    var script = [
      L("l-cmd", "ansible-playbook rolling-update.yml -i inventories/production --limit web", 300),
      L("l-dim", "", 100),
      L("l-play", banner("PLAY [Rolling update — web tier]"), 280),
      L("l-dim", "", 60),
      L("l-task", banner("TASK [Gathering Facts]"), 160),
      L("l-ok", "ok: [web01]", 240),
      L("l-dim", "", 60),
      L("l-task", banner("TASK [Drain node from haproxy]"), 160),
      L("l-changed", "changed: [web01 -> lb01]", 300),
      L("l-dim", "", 60),
      L("l-task", banner("TASK [Wait for active sessions = 0]"), 160),
      L("l-ok", "ok: [web01 -> lb01]", 260),
      L("l-dim", "", 60),
      L("l-task", banner("TASK [app_deploy : Pull release artifact 2.4.1]"), 160),
      L("l-changed", "changed: [web01]", 220),
      L("l-dim", "", 60),
      L("l-task", banner("TASK [app_deploy : Render app config]"), 160),
      L("l-changed", "changed: [web01]", 200),
      L("l-dim", "", 60),
      L("l-task", banner("TASK [nginx : Deploy vhost template]"), 160),
      L("l-changed", "changed: [web01]", 200),
      L("l-dim", "", 60),
      L("l-task", banner("TASK [Verify /healthz returns 200]"), 160),
      L("l-ok", "ok: [web01]", 220),
      L("l-dim", "", 60),
      L("l-task", banner("RUNNING HANDLER [nginx : reload nginx]"), 160),
      L("l-changed", "changed: [web01]", 220),
      L("l-dim", "", 60),
      L("l-task", banner("TASK [Re-enable node in haproxy]"), 160),
      L("l-changed", "changed: [web01 -> lb01]", 320),
      L("l-dim", "", 80),
      L("l-skipped", "── serial: 1 → next host ──", 240),
      L("l-ok", "web02: ok=9  changed=5   web03: ok=9  changed=4", 320),
      L("l-dim", "", 80),
      L("l-recap", banner("PLAY RECAP"), 200),
      L("l-ok", "web01  : ok=9  changed=5  unreachable=0  failed=0  skipped=1", 90),
      L("l-ok", "web02  : ok=9  changed=5  unreachable=0  failed=0  skipped=1", 90),
      L("l-ok", "web03  : ok=9  changed=4  unreachable=0  failed=0  skipped=1", 90)
    ];

    var timer = null, running = false;

    function reset() {
      if (timer) clearTimeout(timer);
      out.textContent = "";
      cursor.classList.remove("done");
      if (statusEl) statusEl.textContent = "streaming…";
    }

    function play() {
      reset();
      running = true;
      var i = 0;
      function next() {
        if (i >= script.length) {
          running = false;
          cursor.classList.add("done");
          if (statusEl) statusEl.textContent = "finished in 1m 12s";
          return;
        }
        var line = script[i++];
        var span = document.createElement("span");
        span.className = line.c;
        span.textContent = line.t;
        out.appendChild(span);
        out.appendChild(document.createTextNode("\n"));
        term.scrollTop = term.scrollHeight;
        timer = setTimeout(next, prefersReduced ? 0 : 40 + line.p);
      }
      next();
    }

    function showAll() {
      reset();
      script.forEach(function (line) {
        var span = document.createElement("span");
        span.className = line.c;
        span.textContent = line.t;
        out.appendChild(span);
        out.appendChild(document.createTextNode("\n"));
      });
      cursor.classList.add("done");
      if (statusEl) statusEl.textContent = "finished in 1m 12s";
    }

    if (replay) replay.addEventListener("click", function () { play(); });

    if (prefersReduced || !("IntersectionObserver" in window)) {
      showAll();
    } else {
      var io = new IntersectionObserver(function (entries) {
        if (entries[0].isIntersecting && !running) {
          io.disconnect();
          play();
        }
      }, { threshold: 0.3 });
      io.observe(term);
    }
  })();

  /* ============================================================
     Inventory browser — tree + merged vars JSON panel
     ============================================================ */
  (function inventory() {
    var treeEl = document.getElementById("inv-tree");
    var jsonEl = document.getElementById("inv-json");
    var pathEl = document.getElementById("inv-path");
    if (!treeEl || !jsonEl) return;

    // [indent, name, type, path, vars]
    var data = [
      [0, "acme", "group", "production / acme", {
        ansible_user: "deploy",
        ansible_python_interpreter: "/usr/bin/python3",
        ntp_servers: ["0.pool.ntp.org", "1.pool.ntp.org"],
        admin_users: ["alice", "bob", "carol"],
        security_hardening: true
      }],
      [1, "frontend", "group", "production / acme / frontend", {
        ansible_user: "deploy",
        firewall_open_ports: [80, 443],
        tls_cert: "/etc/ssl/acme/wildcard.pem",
        security_hardening: true
      }],
      [2, "lb", "group", "production / frontend / lb", {
        haproxy_frontend_port: 443,
        haproxy_stats_port: 8404,
        haproxy_backends: ["web01", "web02", "web03"],
        keepalived_vip: "10.0.10.100"
      }],
      [3, "lb01", "host", "production / lb / lb01", {
        ansible_host: "10.0.10.11",
        keepalived_priority: 101,
        keepalived_state: "MASTER",
        haproxy_frontend_port: 443,
        haproxy_backends: ["web01", "web02", "web03"]
      }],
      [3, "lb02", "host", "production / lb / lb02", {
        ansible_host: "10.0.10.12",
        keepalived_priority: 100,
        keepalived_state: "BACKUP",
        haproxy_frontend_port: 443,
        haproxy_backends: ["web01", "web02", "web03"]
      }],
      [2, "web", "group", "production / frontend / web", {
        nginx_worker_processes: "auto",
        app_name: "acme-shop",
        app_version: "2.4.1",
        app_port: 3000,
        healthcheck_path: "/healthz"
      }],
      [3, "web01", "host", "production / web / web01", {
        ansible_host: "10.0.20.11",
        nginx_worker_processes: 4,
        app_name: "acme-shop",
        app_version: "2.4.1",
        app_port: 3000,
        healthcheck_path: "/healthz",
        env: "production"
      }],
      [3, "web02", "host", "production / web / web02", {
        ansible_host: "10.0.20.12",
        nginx_worker_processes: 4,
        app_name: "acme-shop",
        app_version: "2.4.1",
        app_port: 3000,
        env: "production"
      }],
      [3, "web03", "host", "production / web / web03", {
        ansible_host: "10.0.20.13",
        nginx_worker_processes: 8,
        app_name: "acme-shop",
        app_version: "2.4.1",
        app_port: 3000,
        env: "production",
        node_labels: ["canary"]
      }],
      [1, "backend", "group", "production / acme / backend", {
        backup_enabled: true,
        backup_window: "02:00-03:00",
        monitoring_exporter: true
      }],
      [2, "db", "group", "production / backend / db", {
        postgresql_version: 16,
        postgresql_max_connections: 200,
        postgresql_shared_buffers: "4GB",
        backup_enabled: true
      }],
      [3, "db-primary", "host", "production / db / db-primary", {
        ansible_host: "10.0.30.11",
        postgresql_version: 16,
        postgresql_role: "primary",
        postgresql_max_connections: 200,
        backup_window: "02:00-03:00",
        vault_db_password: "$ANSIBLE_VAULT;1.1;AES256…"
      }],
      [3, "db-replica", "host", "production / db / db-replica", {
        ansible_host: "10.0.30.12",
        postgresql_version: 16,
        postgresql_role: "replica",
        postgresql_primary: "db-primary",
        wal_keep_size: "1GB"
      }],
      [2, "cache", "group", "production / backend / cache", {
        redis_maxmemory: "2gb",
        redis_maxmemory_policy: "allkeys-lru"
      }],
      [3, "redis01", "host", "production / cache / redis01", {
        ansible_host: "10.0.30.21",
        redis_port: 6379,
        redis_maxmemory: "2gb",
        redis_maxmemory_policy: "allkeys-lru",
        redis_appendonly: true
      }],
      [1, "docker_hosts", "group", "production / acme / docker_hosts", {
        docker_edition: "ce",
        docker_compose_v2: true,
        docker_users: ["deploy"],
        compose_stacks: ["registry", "ci-runners"]
      }],
      [2, "dock01", "host", "production / docker_hosts / dock01", {
        ansible_host: "10.0.40.11",
        docker_compose_v2: true,
        compose_stacks: ["registry"],
        docker_data_root: "/var/lib/docker"
      }],
      [2, "dock02", "host", "production / docker_hosts / dock02", {
        ansible_host: "10.0.40.12",
        docker_compose_v2: true,
        compose_stacks: ["ci-runners"],
        docker_data_root: "/mnt/fast/docker"
      }],
      [1, "monitoring", "group", "production / acme / monitoring", {
        prometheus_retention: "30d",
        grafana_port: 3001,
        alert_receivers: ["ops@acme.example"]
      }],
      [2, "mon01", "host", "production / monitoring / mon01", {
        ansible_host: "10.0.50.11",
        prometheus_retention: "30d",
        grafana_port: 3001,
        scrape_targets: 14,
        alert_receivers: ["ops@acme.example"]
      }]
    ];

    function esc(s) {
      return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
    }

    function jsonHTML(obj) {
      var lines = ['<span class="j-punc">{</span>'];
      var keys = Object.keys(obj);
      keys.forEach(function (k, i) {
        var v = obj[k], rendered;
        if (Array.isArray(v)) {
          rendered = '<span class="j-punc">[</span>' + v.map(function (x) {
            return typeof x === "number"
              ? '<span class="j-num">' + x + "</span>"
              : '<span class="j-str">"' + esc(x) + '"</span>';
          }).join('<span class="j-punc">, </span>') + '<span class="j-punc">]</span>';
        } else if (typeof v === "number") {
          rendered = '<span class="j-num">' + v + "</span>";
        } else if (typeof v === "boolean") {
          rendered = '<span class="j-bool">' + v + "</span>";
        } else {
          rendered = '<span class="j-str">"' + esc(v) + '"</span>';
        }
        lines.push('  <span class="j-key">"' + esc(k) + '"</span><span class="j-punc">:</span> ' +
          rendered + (i < keys.length - 1 ? '<span class="j-punc">,</span>' : ""));
      });
      lines.push('<span class="j-punc">}</span>');
      lines.push('<span class="j-comment">// merged: host_vars ← group_vars ← all</span>');
      return lines.join("\n");
    }

    function select(node, row) {
      treeEl.querySelectorAll(".inv-node").forEach(function (el) {
        el.classList.toggle("active", el === node);
      });
      jsonEl.innerHTML = jsonHTML(row[4]);
      if (pathEl) pathEl.textContent = row[3];
    }

    var defaultNode = null;
    data.forEach(function (row) {
      var el = document.createElement("div");
      el.className = "inv-node" + (row[2] === "group" ? " inv-node-group" : "");
      el.setAttribute("role", "treeitem");
      el.setAttribute("tabindex", "0");
      el.style.paddingLeft = (10 + row[0] * 18) + "px";

      var twisty = document.createElement("span");
      twisty.className = "twisty";
      twisty.textContent = row[2] === "group" ? "▾" : "";
      var icon = document.createElement("span");
      icon.className = "inv-icon inv-icon-" + (row[2] === "group" ? "group" : "host");
      var label = document.createElement("span");
      label.textContent = row[1];

      el.appendChild(twisty);
      el.appendChild(icon);
      el.appendChild(label);
      if (row[2] === "group") {
        var count = document.createElement("span");
        count.className = "inv-count";
        count.textContent = "group";
        el.appendChild(count);
      }
      el.addEventListener("click", function () { select(el, row); });
      el.addEventListener("keydown", function (ev) {
        if (ev.key === "Enter" || ev.key === " ") {
          ev.preventDefault();
          select(el, row);
        }
      });
      treeEl.appendChild(el);
      if (row[1] === "web01") defaultNode = { el: el, row: row };
    });
    if (defaultNode) select(defaultNode.el, defaultNode.row);
  })();

  /* ============================================================
     Plan mode — animated three-valued plan. Renders verdict rows,
     types a value into the missing-variables panel, re-plans:
     unknowns collapse into run/skip and the summary updates.
     ============================================================ */
  (function planMode() {
    var tasksEl = document.getElementById("plan-tasks");
    if (!tasksEl) return;

    var runEl = document.getElementById("plan-run");
    var skipEl = document.getElementById("plan-skip");
    var unknownEl = document.getElementById("plan-unknown");
    var unknownChip = document.getElementById("plan-unknown-chip");
    var exitEl = document.getElementById("plan-exit");
    var etaEl = document.getElementById("plan-eta");
    var missingEl = document.getElementById("plan-missing");
    var missingCount = document.getElementById("plan-missing-count");
    var inputVal = document.getElementById("plan-input-val");
    var inputCursor = document.getElementById("plan-cursor");
    var replanBtn = document.getElementById("plan-replan");
    var replayBtn = document.getElementById("plan-replay");
    var panel = tasksEl.closest(".plan-panel");

    var HOSTS = 14; // production inventory
    // b = [run, skip, unknown] before re-plan; a = after (defaults to b)
    var baseRows = [
      { name: "common : Install base packages", b: [14, 0, 0] },
      { name: "common : Configure NTP servers", b: [14, 0, 0] },
      {
        name: "base : Include OS tasks · {{ ansible_facts.os_family }}.yml",
        b: [0, 0, 14], a: [14, 0, 0],
        whenB: { t: "missing: ansible_facts.os_family", c: "pw-missing" },
        whenA: { t: "resolved → debian.yml · 51 tasks planned", c: "pw-resolved" }
      },
      {
        name: "hardening : Configure SELinux",
        b: [0, 0, 14], a: [0, 14, 0],
        whenB: { t: "missing: ansible_facts.os_family", c: "pw-missing" },
        whenA: { t: 'when: ansible_facts.os_family == "RedHat" → false', c: "pw-false" }
      },
      { name: "hardening : Disable root SSH login", b: [14, 0, 0] },
      {
        name: "nginx : Deploy vhost template",
        b: [3, 11, 0],
        whenB: { t: "when: 'web' in group_names → false on 11 hosts", c: "pw-false" }
      },
      {
        name: "postgresql : Tune shared_buffers",
        b: [2, 12, 0],
        whenB: { t: "when: 'db' in group_names → false on 12 hosts", c: "pw-false" }
      },
      {
        name: "app_deploy : Pull release 2.4.1",
        b: [3, 11, 0],
        whenB: { t: "loop: ×3 artifacts · serial: 1 · notifies: restart app", c: "pw-loop" }
      }
    ];
    // rows that only exist once the include above is resolved
    var newRows = [
      { name: "debian : Configure apt pinning", a: [14, 0, 0] },
      {
        name: "debian : Enable unattended-upgrades",
        a: [12, 2, 0],
        whenA: { t: "when: unattended_upgrades | bool → false on 2 hosts", c: "pw-false" }
      }
    ];

    var timers = [];
    function later(fn, ms) { timers.push(setTimeout(fn, prefersReduced ? 0 : ms)); }
    function clearTimers() { timers.forEach(clearTimeout); timers = []; }

    function verdictFor(c) {
      if (c[2] > 0) return ["pv-unknown", "?"];
      if (c[0] > 0 && c[1] > 0) return ["pv-mixed", "mixed"];
      if (c[0] > 0) return ["pv-run", "run"];
      return ["pv-skip", "skip"];
    }
    function countsText(c) {
      var parts = [];
      if (c[0]) parts.push('<span class="c-run">' + c[0] + " ✓</span>");
      if (c[1]) parts.push('<span class="c-skip">' + c[1] + " –</span>");
      if (c[2]) parts.push('<span class="c-unknown">' + c[2] + " ?</span>");
      return parts.join(" ");
    }
    function setRow(r, c, when) {
      var v = verdictFor(c);
      r.elVerdict.className = "plan-verdict " + v[0];
      r.elVerdict.textContent = v[1];
      r.elRun.style.width = (c[0] / HOSTS * 100) + "%";
      r.elSkip.style.width = (c[1] / HOSTS * 100) + "%";
      r.elUnknown.style.width = (c[2] / HOSTS * 100) + "%";
      r.elCounts.innerHTML = countsText(c);
      if (r.elWhen) {
        if (when) {
          r.elWhen.className = "plan-task-when " + when.c;
          r.elWhen.textContent = when.t;
          r.elWhen.style.display = "";
        } else {
          r.elWhen.style.display = "none";
        }
      }
    }
    function buildRow(r, isNew) {
      var el = document.createElement("div");
      el.className = "plan-row" + (isNew ? " plan-row-new" : "");
      r.elVerdict = document.createElement("span");
      var task = document.createElement("div");
      task.className = "plan-task";
      var name = document.createElement("span");
      name.className = "plan-task-name";
      name.textContent = r.name;
      task.appendChild(name);
      if (r.whenB || r.whenA) {
        r.elWhen = document.createElement("span");
        task.appendChild(r.elWhen);
      }
      var bar = document.createElement("span");
      bar.className = "pbar";
      r.elRun = document.createElement("i"); r.elRun.className = "pb-run";
      r.elSkip = document.createElement("i"); r.elSkip.className = "pb-skip";
      r.elUnknown = document.createElement("i"); r.elUnknown.className = "pb-unknown";
      bar.appendChild(r.elRun); bar.appendChild(r.elSkip); bar.appendChild(r.elUnknown);
      r.elCounts = document.createElement("span");
      r.elCounts.className = "plan-counts";
      el.appendChild(r.elVerdict);
      el.appendChild(task);
      el.appendChild(bar);
      el.appendChild(r.elCounts);
      r.el = el;
      return el;
    }
    function moreRow(text) {
      var el = document.createElement("div");
      el.className = "plan-row plan-row-more";
      el.textContent = text;
      return el;
    }
    function countUp(el, from, to, ms) {
      if (prefersReduced || ms <= 0) { el.textContent = String(to); return; }
      var t0 = null;
      function frame(t) {
        if (t0 === null) t0 = t;
        var p = Math.min(1, (t - t0) / ms);
        p = 1 - Math.pow(1 - p, 3);
        el.textContent = String(Math.round(from + (to - from) * p));
        if (p < 1) requestAnimationFrame(frame);
      }
      requestAnimationFrame(frame);
    }

    var moreEl = null, replanned = false, started = false;

    function reset() {
      clearTimers();
      tasksEl.textContent = "";
      replanned = false;
      baseRows.forEach(function (r) {
        tasksEl.appendChild(buildRow(r, false));
        setRow(r, r.b, r.whenB);
      });
      moreEl = moreRow("… 30 more tasks");
      tasksEl.appendChild(moreEl);
      runEl.textContent = "144";
      skipEl.textContent = "12";
      unknownEl.textContent = "28";
      unknownChip.classList.remove("zero");
      exitEl.className = "plan-exit";
      exitEl.textContent = "exit 3 — unknowns present";
      etaEl.textContent = "planned in 9 ms · ~4m 36s est. from past runs";
      missingEl.classList.remove("resolved");
      missingCount.textContent = "×28";
      inputVal.textContent = "";
      inputCursor.classList.remove("done");
      replanBtn.classList.remove("flash");
    }

    function replan() {
      if (replanned) return;
      replanned = true;
      inputVal.textContent = "Debian";
      inputCursor.classList.add("done");
      replanBtn.classList.add("flash");
      later(function () { replanBtn.classList.remove("flash"); }, 320);

      // unknowns collapse to run / skip
      baseRows.forEach(function (r) {
        setRow(r, r.a || r.b, r.whenA || r.whenB);
      });
      // the resolved include pulls newly-planned tasks into the plan
      var anchor = baseRows[3].el; // insert after the include row
      newRows.forEach(function (r, i) {
        var el = buildRow(r, true);
        tasksEl.insertBefore(el, anchor);
        setRow(r, r.a, r.whenA);
        later(function () { el.classList.add("in"); }, 120 + i * 110);
      });
      var moreNew = moreRow("… 49 more tasks from debian.yml");
      moreNew.classList.add("plan-row-new");
      tasksEl.insertBefore(moreNew, anchor);
      later(function () { moreNew.classList.add("in"); }, 340);

      missingEl.classList.add("resolved");
      missingCount.textContent = "✓ 0";
      countUp(runEl, 144, 762, 800);
      countUp(skipEl, 12, 138, 800);
      countUp(unknownEl, 28, 0, 800);
      later(function () {
        unknownChip.classList.add("zero");
        exitEl.className = "plan-exit ok";
        exitEl.textContent = "exit 0 — safe to apply";
        etaEl.textContent = "re-planned in 11 ms · ~11m 02s est. from past runs";
      }, 820);
    }

    function play() {
      reset();
      // stagger rows in
      var all = tasksEl.children;
      Array.prototype.forEach.call(all, function (el, i) {
        later(function () { el.classList.add("in"); }, 80 + i * 90);
      });
      // type the missing value, then re-plan
      var value = "Debian";
      var typeAt = 1900;
      value.split("").forEach(function (ch, i) {
        later(function () { inputVal.textContent = value.slice(0, i + 1); }, typeAt + i * 150);
      });
      later(replan, typeAt + value.length * 150 + 450);
    }

    replayBtn.addEventListener("click", play);
    replanBtn.addEventListener("click", function () {
      clearTimers();
      Array.prototype.forEach.call(tasksEl.children, function (el) { el.classList.add("in"); });
      replan();
    });

    if (prefersReduced || !("IntersectionObserver" in window)) {
      play();
    } else {
      var io = new IntersectionObserver(function (entries) {
        if (entries[0].isIntersecting && !started) {
          started = true;
          io.disconnect();
          play();
        }
      }, { threshold: 0.25 });
      io.observe(panel);
    }
  })();

  /* ============================================================
     Drift heatmap — playbooks × hosts, --check replays
     ============================================================ */
  (function drift() {
    var table = document.getElementById("drift");
    if (!table) return;
    var readout = document.getElementById("drift-readout");
    var DEFAULT = "web02 × webservers.yml — nginx : Deploy config · changed under --check";

    var hosts = ["lb01", "lb02", "web01", "web02", "web03", "db-primary",
      "db-replica", "redis01", "dock01", "dock02", "mon01"];
    var rows = [
      {
        pb: "site.yml", targets: null,
        cells: {
          web02: ["warn", "nginx : Deploy config · changed under --check"],
          dock01: ["warn", "docker : Configure daemon.json · changed under --check"]
        }
      },
      {
        pb: "webservers.yml", targets: ["lb01", "lb02", "web01", "web02", "web03"],
        cells: {
          web02: ["warn", "nginx : Deploy config · changed under --check"],
          web03: ["bad", "nginx : Validate config · failed under --check"]
        }
      },
      {
        pb: "hardening.yml", targets: null,
        cells: { "db-replica": ["warn", "hardening : Configure sysctl · changed under --check"] }
      },
      {
        pb: "databases.yml", targets: ["db-primary", "db-replica", "redis01"],
        cells: { "db-replica": ["warn", "postgresql : Render pg_hba.conf · changed under --check"] }
      },
      { pb: "monitoring.yml", targets: null, cells: {} }
    ];

    var thead = document.createElement("thead");
    var hr = document.createElement("tr");
    var corner = document.createElement("th");
    corner.className = "drift-pb";
    hr.appendChild(corner);
    hosts.forEach(function (h) {
      var th = document.createElement("th");
      th.textContent = h;
      hr.appendChild(th);
    });
    thead.appendChild(hr);
    table.appendChild(thead);

    var tbody = document.createElement("tbody");
    rows.forEach(function (row) {
      var tr = document.createElement("tr");
      var label = document.createElement("td");
      label.className = "drift-pb";
      label.textContent = row.pb;
      tr.appendChild(label);
      hosts.forEach(function (h) {
        var td = document.createElement("td");
        var cell = document.createElement("span");
        var targeted = !row.targets || row.targets.indexOf(h) !== -1;
        var state = "ok", msg = "in sync · last --check 18m ago";
        if (!targeted) {
          state = "na"; msg = "not targeted by this playbook";
        } else if (row.cells[h]) {
          state = row.cells[h][0]; msg = row.cells[h][1];
        }
        cell.className = "dcell d-" + state;
        var text = h + " × " + row.pb + " — " + msg;
        cell.title = text;
        cell.addEventListener("mouseenter", function () {
          if (readout) readout.textContent = text;
        });
        cell.addEventListener("mouseleave", function () {
          if (readout) readout.textContent = DEFAULT;
        });
        td.appendChild(cell);
        tr.appendChild(td);
      });
      tbody.appendChild(tr);
    });
    table.appendChild(tbody);
  })();
})();
