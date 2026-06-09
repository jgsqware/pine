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

    // id, parent, type, radius
    var spec = [
      ["acme", null, "group", 27],
      ["frontend", "acme", "group", 21],
      ["backend", "acme", "group", 21],
      ["docker_hosts", "acme", "group", 19],
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
        id: s[0], parent: s[1], type: s[2], r: s[3],
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
      g.setAttribute("class", "node node-" + n.type);
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
      return n.id + " — group · " + hostCount + " host" + (hostCount === 1 ? "" : "s") +
        (kids.length ? " · children: " + kids.join(", ") : "");
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
})();
