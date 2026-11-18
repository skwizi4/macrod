// Dashboard shell: data loading, routing, themes and chart drawing. Classic
// script on purpose: ES modules do not load from file://.
window.Macro = (function () {
  "use strict";
  var I18N = window.MacroI18n, t = I18N.t;
  var isStatic = window.MACRO_MODE === "static";
  var isFile = location.protocol === "file:";
  var HIDDEN_TEXT = t("Hidden in the demo (source license required)");

  var state = { meta: null, region: null, page: null, range: "3", charts: [], route: 0 };
  var cache = new Map();

  // euSources names the European publishers whose series the demo shows
  // (their reuse terms ask for the source, D24).
  function euSources(meta) {
    var names = { ecb: "European Central Bank (ECB Data Portal)", bundesbank: "Deutsche Bundesbank", eurostat: "Eurostat (CC BY 4.0)" };
    var seen = {};
    meta.regions.forEach(function (r) {
      (r.pages || []).forEach(function (p) {
        (p.charts || []).forEach(function (c) {
          (c.series || []).forEach(function (x) { if (!x.hidden && names[x.source]) seen[x.source] = true; });
        });
      });
    });
    var out = ["ecb", "bundesbank", "eurostat"].filter(function (k) { return seen[k]; }).map(function (k) { return names[k]; });
    return out.length ? ", " + out.join(", ") : "";
  }

  function get(key, fallback) { try { return localStorage.getItem(key) || fallback; } catch (e) { return fallback; } }
  function put(key, value) { try { localStorage.setItem(key, value); } catch (e) { /* private window */ } }

  // enc makes a path segment safe for files and static hosts; the same rule
  // is in internal/dashboard/site.go.
  function enc(segment) {
    var out = "";
    new TextEncoder().encode(segment).forEach(function (b) {
      var c = String.fromCharCode(b);
      out += /[A-Za-z0-9_-]/.test(c) ? c : "~" + (b < 16 ? "0" : "") + b.toString(16).toUpperCase();
    });
    return out;
  }

  // Answers of file:// data scripts arrive here.
  var waiting = new Map();
  window.__macroData = function (path, data) {
    var done = waiting.get(path);
    if (done) { waiting.delete(path); done(data); }
  };
  function loadScript(path) {
    return new Promise(function (resolve, reject) {
      waiting.set(path, resolve);
      var s = document.createElement("script");
      s.src = "data/" + path + ".js";
      s.onerror = function () { waiting.delete(path); reject(new Error("no data")); };
      document.head.appendChild(s);
    });
  }
  function fetchJSON(url) {
    return fetch(url).then(function (r) {
      if (!r.ok) throw new Error("HTTP " + r.status);
      return r.json();
    });
  }
  // load reads one answer: parts is ["series", source, id, code] and so on.
  function load(parts, query) {
    var path = parts.map(enc).join("/");
    var cacheKey = path + (isStatic ? "" : "?" + (query || ""));
    if (!cache.has(cacheKey)) {
      var p;
      if (isStatic) p = isFile ? loadScript(path) : fetchJSON("api/" + path + ".json");
      else p = fetchJSON(liveURL(parts, query));
      p = p.catch(function (e) { cache.delete(cacheKey); throw e; });
      cache.set(cacheKey, p);
    }
    return cache.get(cacheKey);
  }
  function liveURL(parts, query) {
    var seg = parts.slice();
    var q = query ? "?" + query : "";
    if (seg[0] === "series") {
      var code = seg.pop();
      return "api/" + seg.map(encodeURIComponent).join("/") + "?transform=" + encodeURIComponent(code) + (query ? "&" + query : "");
    }
    if (seg[0] === "reports") {
      var week = seg.pop();
      q = week === "latest" ? "" : "?week=" + encodeURIComponent(week);
    }
    return "api/" + seg.map(encodeURIComponent).join("/") + q;
  }

  function fromDate(range) {
    if (range === "all") return "";
    var d = new Date();
    d.setFullYear(d.getFullYear() - parseInt(range, 10));
    return d.toISOString().slice(0, 10);
  }
  // series returns one series from the date, or whole for "". part splits a
  // series published ahead at the data date: "actual" up to it, "forecast"
  // after it (with the last actual point, so the lines meet).
  // localize puts the English titles and labels of the config (title_en,
  // label_en) in place of the original ones; key rows keep their label, the
  // key of the report rows, and are shown through I18N.pick.
  function localize(meta) {
    meta.regions.forEach(function (r) {
      r.pages.forEach(function (p) {
        p.title = I18N.pick(p, "title");
        p.charts.forEach(function (c) {
          c.title = I18N.pick(c, "title");
          c.series.forEach(function (x) { x.label = I18N.pick(x, "label"); });
        });
      });
    });
    return meta;
  }
  function series(source, id, code, from, part) {
    return load(["series", source, id, code], from ? "from=" + from : "").then(function (s) {
      if (I18N.lang() === "en" && s.label_en) s = Object.assign({}, s, { label: s.label_en });
      var pts = isStatic && from ? s.points.filter(function (p) { return p[0] >= from; }) : s.points;
      if (part) {
        var today = (state.meta.generated_at || new Date().toISOString()).slice(0, 10);
        var past = pts.filter(function (p) { return p[0] <= today; });
        pts = part === "actual" ? past : past.slice(-1).concat(pts.filter(function (p) { return p[0] > today; }));
      }
      return pts === s.points ? s : Object.assign({}, s, { points: pts });
    });
  }
  function report(region, agent, kind, week) {
    return load(["reports", region, agent, week || "latest"]).then(function (r) {
      var f = r.files.filter(function (x) { return x.report === kind; })[0];
      return f && f.json ? f.json : null;
    });
  }

  // DOM helper: text is always text, never HTML.
  function h(tag, props) {
    var el = document.createElement(tag);
    Object.keys(props || {}).forEach(function (k) {
      if (k === "class") el.className = props[k];
      else if (k.indexOf("on") === 0) el.addEventListener(k.slice(2), props[k]);
      else if (props[k] !== undefined && props[k] !== null && props[k] !== false) el.setAttribute(k, props[k] === true ? "" : props[k]);
    });
    for (var i = 2; i < arguments.length; i++) {
      [].concat(arguments[i]).forEach(function (kid) {
        if (kid === null || kid === undefined || kid === false) return;
        el.appendChild(kid.nodeType ? kid : document.createTextNode(String(kid)));
      });
    }
    return el;
  }

  var NB = " ";
  function num(v, dec) {
    var text = v.toLocaleString(I18N.locale(), { minimumFractionDigits: dec, maximumFractionDigits: dec });
    return /^-[0,]+$/.test(text) ? text.slice(1) : text.replace("-", "−"); // no "−0,00"
  }
  // fmt writes a value with its unit, as the reports do.
  function fmt(v, unit, signed) {
    if (v === null || v === undefined) return "—";
    var sign = signed && v > 0 ? "+" : "";
    switch (unit) {
      case "percent": return sign + num(v, Math.abs(v) >= 100 ? 0 : 2) + "%";
      case "pp": return sign + num(v, 2) + NB + t("pp");
      case "bp": return sign + num(v, 0) + NB + t("bp");
      case "usd_bn": return sign + num(v, Math.abs(v) >= 100 ? 0 : 1) + NB + t("bn") + NB + "$";
      case "eur_bn": return sign + num(v, Math.abs(v) >= 100 ? 0 : 1) + NB + t("bn") + NB + "€";
      case "eur": return sign + num(v, 2) + NB + "€";
      case "thousands_persons": return sign + num(v, 0) + NB + t("k");
      case "count": return sign + num(v, 0);
      case "usd": return sign + num(v, 2) + NB + "$";
      case "ratio": return sign + num(v, 2);
      case "z": case "sigma": return sign + num(v, 1) + "σ";
      case "moves_25bp": // positive is cuts, as in the reports
        if (signed) return sign + num(v, 1) + NB + t("moves");
        if (Math.abs(v) < 0.05) return t("no change");
        return "~" + num(Math.abs(v), 1) + NB + (v > 0 ? t("cuts") : t("hikes"));
    }
    return sign + num(v, Math.abs(v) >= 1000 ? 0 : 2);
  }
  function q(o, signed) { return o ? fmt(o.value, o.unit, signed) : "—"; }

  // Canvas text does not inherit CSS, so charts name the font themselves.
  var FONT = '"Inter Variable", system-ui, -apple-system, "Segoe UI", sans-serif';
  function css(name) { return getComputedStyle(document.documentElement).getPropertyValue(name).trim(); }
  function palette() { return [1, 2, 3, 4, 5, 6, 7, 8].map(function (i) { return css("--s" + i); }); }
  function alpha(hex, a) {
    var n = parseInt(hex.slice(1), 16);
    return "rgba(" + (n >> 16) + "," + (n >> 8 & 255) + "," + (n & 255) + "," + a + ")";
  }

  function axisStyle() {
    return {
      axisLine: { lineStyle: { color: css("--ink-3") } },
      axisTick: { show: false },
      axisLabel: { color: css("--ink-3"), hideOverlap: true, fontFamily: FONT, fontSize: 11 },
      splitLine: { lineStyle: { color: css("--grid") } },
    };
  }
  // ruler gives a time axis the ticks of the page ruler.
  function ruler() {
    var tick = { color: css("--ink-3"), width: 1, opacity: 0.7 };
    return { axisTick: { show: true, length: 6, lineStyle: tick }, minorTick: { show: true, splitNumber: 6, length: 3, lineStyle: tick } };
  }
  // MOTION is the longest any transition runs, in ms.
  var MOTION = 280;
  function draw(el, option) {
    var chart = echarts.init(el);
    el.classList.remove("loading");
    option.color = palette();
    option.textStyle = { fontFamily: FONT, color: css("--ink") };
    option.animation = !reducedMotion();
    option.animationDuration = MOTION;
    option.animationEasing = "cubicOut";
    option.animationDurationUpdate = 0;
    if (!option.tooltip) option.tooltip = {};
    option.tooltip.backgroundColor = css("--surface");
    option.tooltip.borderColor = css("--line");
    option.tooltip.borderWidth = 1;
    option.tooltip.padding = [6, 10];
    option.tooltip.textStyle = { color: css("--ink"), fontFamily: FONT, fontSize: 12 };
    option.tooltip.extraCssText = "border-radius:2px;box-shadow:0 6px 18px rgba(0,0,0,.24)";
    option.tooltip.confine = true;
    if (option.legend) { option.legend.textStyle = { color: css("--ink-2"), fontFamily: FONT, fontSize: 12 }; option.legend.type = "scroll"; option.legend.pageTextStyle = { color: css("--ink-2") }; option.legend.icon = "rect"; option.legend.itemWidth = 12; option.legend.itemHeight = 8; }
    chart.setOption(option);
    var ro = new ResizeObserver(function () { chart.resize(); });
    ro.observe(el);
    state.charts.push({ chart: chart, ro: ro });
    return chart;
  }
  function reducedMotion() { return window.matchMedia("(prefers-reduced-motion: reduce)").matches; }
  function dropChart(chart) {
    state.charts = state.charts.filter(function (c) {
      if (c.chart !== chart) return true;
      c.ro.disconnect();
      c.chart.dispose();
      return false;
    });
  }
  function clearCharts() {
    state.charts.forEach(function (c) { c.ro.disconnect(); c.chart.dispose(); });
    state.charts = [];
  }
  function tooltipValue(unit) { return function (v) { return fmt(Array.isArray(v) ? v[1] : v, unit); }; }
  function axisLabel(unit) {
    return function (v) {
      switch (unit) {
        case "percent": return num(v, 1) + "%";
        case "bp": return num(v, 0);
        case "usd_bn": case "eur_bn": return Math.abs(v) >= 1000 ? num(v / 1000, 1) + NB + t("tn") : num(v, 0);
        case "thousands_persons": return num(v, 0);
        case "ratio": return num(v, 2);
      }
      return Math.abs(v) >= 1000 ? num(v, 0) : num(v, Math.abs(v) >= 10 ? 0 : 2);
    };
  }
  function zoomOf() {
    // Slider only: an "inside" zoom takes the finger from the page scroll.
    var ink = css("--ink-2");
    return [{
      type: "slider", height: 6, bottom: 8, brushSelect: false, showDetail: false, showDataShadow: false, moveHandleSize: 0,
      borderColor: "transparent", backgroundColor: "transparent", fillerColor: alpha(ink, 0.12),
      handleSize: "200%", handleStyle: { color: ink, borderColor: ink },
    }];
  }
  function crosshair() {
    var line = { color: css("--ink-3"), type: "solid", width: 1 };
    return {
      type: "cross", lineStyle: line, crossStyle: line,
      label: { backgroundColor: css("--surface"), borderColor: css("--line"), borderWidth: 1, borderRadius: 2, color: css("--ink"), fontFamily: FONT, fontSize: 11 },
    };
  }
  // lastMark writes the latest value at the right end of the line. The text
  // stays in ink on the page colour: the outline carries the series colour.
  function lastMark(points, unit, color) {
    return {
      symbol: "none", silent: true, animation: false, precision: 6,
      data: [{ yAxis: points[points.length - 1][1] }],
      lineStyle: { color: alpha(color, 0.5), type: "dotted", width: 1 },
      label: { position: "end", formatter: function (p) { return axisLabel(unit)(p.value); }, backgroundColor: css("--plane"), borderColor: color, borderWidth: 1, color: css("--ink"), fontFamily: FONT, fontSize: 11, fontWeight: 600, padding: [3, 5], borderRadius: 2 },
    };
  }
  function timeChart(el, kind, data) {
    var unit = data[0].unit;
    var single = data.length === 1;
    var color = css("--s1");
    var option = {
      grid: { left: 4, right: 6, top: 8, bottom: 30, containLabel: true },
      tooltip: { trigger: "axis", valueFormatter: tooltipValue(unit), axisPointer: crosshair() },
      legend: { show: false },
      xAxis: Object.assign({ type: "time" }, axisStyle(), ruler(), { splitLine: { show: false } }),
      yAxis: Object.assign({ type: "value", position: "right", scale: kind !== "bar", splitNumber: 4 }, axisStyle(), { axisLine: { show: false } }),
      dataZoom: zoomOf(),
      series: data.map(function (s) {
        if (kind === "bar") return { name: s.name, type: "bar", data: s.points, barMaxWidth: 10, large: s.points.length > 600 };
        var line = { name: s.name, type: "line", data: s.points, showSymbol: false, sampling: "lttb", lineStyle: { width: 1.75 } };
        if (single && s.points.length) {
          // A lone series gets a faint flat wash; several lines stay bare.
          line.areaStyle = { origin: "start", color: alpha(color, 0.07) };
          line.markLine = lastMark(s.points, unit, color);
        }
        return line;
      }),
    };
    option.yAxis.axisLabel = Object.assign({}, option.yAxis.axisLabel, { formatter: axisLabel(unit) });
    option.xAxis.axisPointer = { label: { formatter: function (p) { return new Date(p.value).toISOString().slice(0, 10); } } };
    option.yAxis.axisPointer = { label: { formatter: function (p) { return axisLabel(unit)(p.value); } } };
    return draw(el, option);
  }

  // valueAt is the last value not later than date.
  function valueAt(points, date) {
    for (var i = points.length - 1; i >= 0; i--) if (points[i][0] <= date) return points[i];
    return null;
  }
  function curveChart(el, chart, data) {
    var last = data.reduce(function (m, s) { var p = s.points[s.points.length - 1]; return p && p[0] > m ? p[0] : m; }, "");
    var d = new Date(last + "T00:00:00Z");
    d.setUTCDate(d.getUTCDate() - chart.compare_days);
    var earlier = d.toISOString().slice(0, 10);
    function values(date) { return data.map(function (s) { var p = valueAt(s.points, date); return p ? p[1] : null; }); }
    var chart2 = draw(el, {
      grid: { left: 8, right: 8, top: 10, bottom: 8, containLabel: true },
      tooltip: { trigger: "axis", valueFormatter: tooltipValue("percent") },
      legend: { show: false },
      xAxis: Object.assign({ type: "category", data: data.map(function (s) { return s.name; }) }, axisStyle(), { splitLine: { show: false } }),
      yAxis: (function () {
        var axis = Object.assign({ type: "value", position: "right", scale: true, splitNumber: 4 }, axisStyle(), { axisLine: { show: false } });
        axis.axisLabel = Object.assign({}, axis.axisLabel, { formatter: axisLabel("percent") });
        return axis;
      })(),
      series: [
        { name: last, type: "line", data: values(last), symbolSize: 6, lineStyle: { width: 1.75 } },
        { name: earlier, type: "line", data: values(earlier), symbolSize: 6, lineStyle: { width: 1.75, type: "dashed" } },
      ],
    });
    return { chart: chart2, names: [last, earlier] };
  }

  function plaque(text) { return h("div", { class: "plaque" }, text || HIDDEN_TEXT); }

  // change is a signed value coloured by direction, as market screens do:
  // the colour says up or down, not good or bad.
  function change(text, value) {
    var moved = /[1-9]/.test(text); // what rounds to zero did not move
    return h("span", { class: "chg" + (moved && value > 0 ? " up" : moved && value < 0 ? " down" : "") }, text);
  }
  function pointIndex(points, date) {
    for (var i = points.length - 1; i >= 0; i--) if (points[i][0] <= date) return i;
    return -1;
  }
  function headline(box, points, unit, i) {
    box.replaceChildren(h("span", { class: "now" }, fmt(points[i][1], unit)));
    if (i > 0) {
      var d = points[i][1] - points[i - 1][1];
      box.appendChild(change(fmt(d, unit === "percent" ? "pp" : unit, true), d));
    }
  }
  // follow calls show with the date under the crosshair, and with "" when
  // the pointer leaves the chart.
  function follow(chart, show) {
    var last = "";
    chart.on("updateAxisPointer", function (e) {
      var axis = (e.axesInfo || []).filter(function (a) { return a.axisDim === "x"; })[0];
      if (!axis) return;
      var date = new Date(axis.value).toISOString().slice(0, 10);
      if (date !== last) { last = date; show(date); }
    });
    chart.getZr().on("globalout", function () { last = ""; show(""); });
  }
  // chips is the series legend: a name with its colour and, for lines over
  // time, a value. A click hides or shows the series.
  function chips(chart, items) {
    var colors = palette();
    var values = [];
    var el = h("div", { class: "chips" }, items.map(function (item, i) {
      var b = item.value ? h("b", null, item.value) : null;
      values.push(b);
      return h("button", {
        type: "button", class: "chip", style: "--c:" + colors[i], "aria-pressed": "true",
        onclick: function (e) {
          var pressed = e.currentTarget;
          pressed.setAttribute("aria-pressed", pressed.getAttribute("aria-pressed") === "true" ? "false" : "true");
          chart.dispatchAction({ type: "legendToggleSelect", name: item.name });
        },
      }, h("i"), item.name, b);
    }));
    return { el: el, set: function (i, text) { if (values[i]) values[i].textContent = text; } };
  }
  // wire shows the latest values of a time chart and follows the crosshair:
  // the headline for one series, the legend for several.
  function wire(chart, data, box, asof, host, before) {
    var latest = data.map(function (s) { return s.points.length ? s.points[s.points.length - 1][0] : ""; }).sort().pop();
    var at;
    if (data.length === 1) {
      var points = data[0].points;
      at = function (date) {
        var i = date ? pointIndex(points, date) : points.length - 1;
        if (i >= 0) { headline(box, points, data[0].unit, i); asof.textContent = t("as of {0}", points[i][0]); }
      };
    } else {
      var row = chips(chart, data.map(function (s) {
        var p = s.points[s.points.length - 1];
        return { name: s.name, value: p ? fmt(p[1], s.unit) : "—" };
      }));
      host.insertBefore(row.el, before);
      at = function (date) {
        data.forEach(function (s, i) {
          var j = date ? pointIndex(s.points, date) : s.points.length - 1;
          row.set(i, j >= 0 ? fmt(s.points[j][1], s.unit) : "—");
        });
        asof.textContent = t("as of {0}", date || latest);
      };
    }
    at("");
    follow(chart, at);
  }

  // select is a dropdown drawn by the page: a native one cannot be styled.
  // options are [value, text].
  function select(o) {
    var value = o.value;
    var text = h("span");
    var button = h("button", { type: "button", class: "pick-btn", "aria-haspopup": "listbox", "aria-expanded": "false", "aria-label": o.label }, text);
    var list = h("ul", { class: "pick-list", role: "listbox", tabindex: "-1", hidden: true });
    function setOpen(open) {
      list.hidden = !open;
      button.setAttribute("aria-expanded", open ? "true" : "false");
      if (open) {
        document.addEventListener("pointerdown", away);
        var on = list.querySelector("[aria-selected=true]") || list.firstChild;
        if (on) on.focus();
      } else {
        document.removeEventListener("pointerdown", away);
      }
    }
    function away(e) { if (!root.contains(e.target)) setOpen(false); }
    function choose(v) {
      setOpen(false);
      button.focus();
      if (v === value) return;
      value = v;
      paint();
      o.onChange(v);
    }
    function paint() {
      var current = o.options.filter(function (x) { return x[0] === value; })[0];
      text.textContent = current ? current[1] : "";
      list.replaceChildren.apply(list, o.options.map(function (x) {
        return h("li", { role: "option", tabindex: "-1", "aria-selected": x[0] === value ? "true" : "false", onclick: function () { choose(x[0]); } }, x[1]);
      }));
    }
    list.addEventListener("keydown", function (e) {
      var at = document.activeElement;
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); setOpen(false); button.focus(); }
      else if (e.key === "Tab") setOpen(false);
      else if (e.key === "ArrowDown" && at.nextSibling) { e.preventDefault(); at.nextSibling.focus(); }
      else if (e.key === "ArrowUp" && at.previousSibling) { e.preventDefault(); at.previousSibling.focus(); }
      else if (e.key === "Enter" || e.key === " ") { e.preventDefault(); at.click(); }
    });
    button.addEventListener("click", function () { setOpen(list.hidden); });
    var root = h("div", { class: "pick" }, button, list);
    paint();
    return { el: root, set: function (v) { value = v; paint(); } };
  }

  function rangeButtons(current, pick) {
    var box = h("div", { class: "ranges" });
    [["1", t("1Y")], ["3", t("3Y")], ["5", t("5Y")], ["all", t("All")]].forEach(function (r) {
      box.appendChild(h("button", { type: "button", class: current === r[0] ? "on" : "", onclick: function () { pick(r[0]); } }, r[1]));
    });
    return box;
  }
  function statsOf(data) {
    var s = data[0], points = s.points;
    if (data.length !== 1 || points.length < 2) return [];
    var values = points.map(function (p) { return p[1]; });
    var d = values[values.length - 1] - values[0];
    return [
      [t("Change over the period"), change(fmt(d, s.unit === "percent" ? "pp" : s.unit, true), d)],
      [t("Min"), fmt(Math.min.apply(null, values), s.unit)],
      [t("Max"), fmt(Math.max.apply(null, values), s.unit)],
      [t("Since"), points[0][0]],
    ];
  }
  function statRow(items) {
    return h("dl", { class: "stats" }, items.map(function (x) { return h("div", null, h("dt", null, x[0]), h("dd", null, x[1])); }));
  }

  // inspect opens one metric or chart in a large window. o: title, kind,
  // series ([{source, id, transform, label}], may be empty), facts ([label,
  // text], values of the weekly report).
  function inspect(o) {
    var range = state.range, token = 0, chart = null;
    var series_ = o.series || [];
    var box = h("div", { class: "readout big" });
    var asof = h("span", { class: "asof" });
    var body = h("div", { class: "sheet-body" });
    var dlg = h("dialog", { class: "sheet", "aria-label": o.title },
      h("div", { class: "sheet-in" },
        h("header", { class: "sheet-head" },
          h("div", null, h("h2", null, o.title), box),
          h("div", { class: "head-right" }, asof,
            h("button", { type: "button", class: "close", "aria-label": t("Close"), onclick: function () { dlg.close(); } }))),
        body,
        o.facts ? statRow(o.facts) : null));
    function drop() { if (chart) { dropChart(chart); chart = null; } }
    dlg.addEventListener("close", function () { token++; drop(); dlg.remove(); });
    dlg.addEventListener("click", function (e) { if (e.target === dlg) dlg.close(); });
    function paint() {
      var mine = ++token;
      drop();
      var el = h("div", { class: "chart sheet-chart loading" });
      var stats = h("div");
      body.replaceChildren(rangeButtons(range, function (r) { range = r; paint(); }), el, stats);
      var from = fromDate(range);
      Promise.all(series_.map(function (s) { return series(s.source, s.id, s.transform, s.part === "forecast" ? "" : from, s.part); })).then(function (all) {
        if (mine !== token) return;
        var data = all.map(function (s, i) { return { name: series_[i].label, unit: s.unit, points: s.points }; });
        if (data.every(function (s) { return !s.points.length; })) { el.replaceWith(h("p", { class: "muted" }, t("No data for the period."))); return; }
        chart = timeChart(el, o.kind === "bar" ? "bar" : "line", data);
        wire(chart, data, box, asof, body, el);
        var items = statsOf(data);
        if (items.length) stats.replaceWith(statRow(items));
      }).catch(function () {
        if (mine === token) el.replaceWith(h("p", { class: "err" }, t("Failed to load data.")));
      });
    }
    document.body.appendChild(dlg);
    dlg.showModal();
    if (series_.length) paint();
  }
  // countUp runs a number up to its value once, when a page opens.
  function countUp(el, value, unit) {
    if (value === null || value === undefined || reducedMotion()) return;
    var start = performance.now(), final = fmt(value, unit);
    (function tick(now) {
      var k = Math.min((now - start) / MOTION, 1);
      el.textContent = k < 1 ? fmt(value * (1 - Math.pow(1 - k, 3)), unit) : final;
      if (k < 1) requestAnimationFrame(tick);
    })(start);
  }

  function expandButton(open, title) {
    return h("button", { type: "button", class: "expand", "aria-label": t("Open large: {0}", title), title: t("Open large"), onclick: open });
  }
  function renderChart(chart, region) {
    if (chart.hidden) return plaque(h("span", null, chart.title + ": " + HIDDEN_TEXT));
    var el = h("div", { class: "chart loading" });
    var note = h("p", { class: "note" });
    var box = h("div", { class: "readout", title: t("Change from the previous observation") });
    var asofEl = h("span", { class: "asof" });
    var right = h("div", { class: "head-right" }, asofEl);
    var top = h("header", { class: "panel-head" }, h("div", null, h("h2", null, chart.title), box), right);
    var card = h("section", { class: "card" }, top, el, note);
    var shown = chart.series.filter(function (s) { return !s.hidden; });
    if (chart.kind !== "curve") right.appendChild(expandButton(function () { inspect({ title: chart.title, kind: chart.kind, series: shown }); }, chart.title));
    if (shown.length < chart.series.length) note.textContent = t("Some series are hidden in the demo.");
    var route = state.route;
    var from = chart.kind === "curve" ? fromDate("1") : fromDate(state.range);
    Promise.all(shown.map(function (s) { return series(s.source, s.id, s.transform, s.part === "forecast" ? "" : from, s.part); })).then(function (all) {
      if (route !== state.route) return;
      var data = all.map(function (s, i) { return { name: shown[i].label, unit: s.unit, points: s.points }; });
      if (data.every(function (s) { return s.points.length === 0; })) { el.remove(); note.textContent = t("No data for the period."); return; }
      var ends = data.map(function (s) { return s.points.length ? s.points[s.points.length - 1][0] : ""; }).sort();
      if (chart.kind === "curve") {
        var curve = curveChart(el, chart, data);
        card.insertBefore(chips(curve.chart, curve.names.map(function (n) { return { name: n }; })).el, el);
        asofEl.textContent = t("as of {0}", ends[ends.length - 1]);
      } else {
        wire(timeChart(el, chart.kind, data), data, box, asofEl, card, el);
      }
    }).catch(function () {
      if (route !== state.route) return;
      el.remove();
      note.className = "note err";
      note.textContent = t("Failed to load data.");
    });
    return card;
  }

  // dateline is the line over a page title: region, ISO week and the date of
  // the data (the build date of a static site, today for the live one).
  function dateline() {
    var d = state.meta.generated_at ? new Date(state.meta.generated_at) : new Date();
    var thu = new Date(Date.UTC(d.getFullYear(), d.getMonth(), d.getDate()));
    thu.setUTCDate(thu.getUTCDate() + 4 - (thu.getUTCDay() || 7)); // the Thursday of the week
    var week = Math.ceil(((thu - Date.UTC(thu.getUTCFullYear(), 0, 1)) / 864e5 + 1) / 7);
    return h("div", { class: "dateline" },
      h("span", null, state.region.id),
      h("span", null, t("Week {0}", week)),
      h("span", null, d.toLocaleDateString(I18N.locale(), { day: "numeric", month: "long", year: "numeric" })));
  }

  function renderPage() {
    clearCharts();
    state.route++;
    var main = document.getElementById("page");
    main.textContent = "";
    var page = state.page;
    var first = state.region.pages[0] === page;
    var hasCharts = page.charts.some(function (c) { return c.kind !== "curve" && !c.hidden; });
    var cover = state.meta.demo && first;
    if (cover) {
      main.appendChild(h("section", { class: "intro" }, dateline(),
        h("h1", null, t("Macroeconomics "), h("span", null, t("at a glance"))),
        h("p", null, t("Liquidity, rates, inflation, the budget and company reporting. Collected daily from official sources.")),
        h("div", { class: "rule" })));
    }
    var row = h("div", { class: "head-row" }, h(cover ? "h2" : "h1", { class: "title" }, page.title));
    if (hasCharts) row.appendChild(rangeButtons(state.range, function (r) { state.range = r; put("macro.range", r); renderPage(); }));
    main.appendChild(h("header", { class: "head" }, cover ? null : dateline(), row, cover ? null : h("div", { class: "rule" })));
    if (page.charts.length) {
      main.appendChild(h("div", { class: "grid" }, page.charts.map(function (c) { return renderChart(c, state.region); })));
    }
    page.views.forEach(function (v) {
      if (v.hidden) { main.appendChild(plaque(h("span", null, (Macro.viewTitles[v.view] || v.view) + ": " + HIDDEN_TEXT))); return; }
      var slot = h("div", null, h("div", { class: "card skeleton" }));
      main.appendChild(slot);
      var route = state.route;
      Promise.resolve().then(function () { return Macro.views[v.view](slot, v, state.region, route); }).catch(function () {
        if (route === state.route) slot.replaceChildren(h("div", { class: "card err" }, t("Failed to load data: {0}", Macro.viewTitles[v.view] || v.view)));
      });
    });
  }

  function renderMenu() {
    var menu = document.getElementById("menu");
    menu.textContent = "";
    state.region.pages.forEach(function (p) {
      menu.appendChild(h("a", { href: "#/" + state.region.id + "/" + p.id, class: p === state.page ? "on" : "", "aria-current": p === state.page ? "page" : null }, p.title));
    });
    fitMenu();
  }
  // fitMenu turns the bar into a burger when the region's pages do not fit
  // in one row next to the region and theme controls (wide screens; below
  // 1180px the bar is always compact).
  function fitMenu() {
    var bar = document.getElementById("rail");
    var menu = document.getElementById("menu");
    if (window.matchMedia("(max-width: 1180px)").matches) {
      bar.classList.remove("compact");
      return;
    }
    var wasCompact = bar.classList.contains("compact");
    bar.classList.remove("compact");
    var fits = menu.scrollWidth <= menu.clientWidth + 1;
    bar.classList.toggle("compact", !fits);
    if (wasCompact && fits) setMenuOpen(false);
  }
  function setMenuOpen(open) {
    document.getElementById("rail").classList.toggle("open", open);
    document.getElementById("burger").setAttribute("aria-expanded", open ? "true" : "false");
  }

  function route() {
    var parts = location.hash.replace(/^#\/?/, "").split("/");
    var regions = state.meta.regions;
    var region = regions.filter(function (r) { return r.id === parts[0]; })[0] || regions[0];
    var page = region.pages.filter(function (p) { return p.id === parts[1]; })[0] || region.pages[0];
    state.region = region;
    state.page = page;
    var open = document.querySelector("dialog[open]");
    if (open) open.close();
    state.regionPick.set(region.id);
    renderMenu();
    setMenuOpen(false);
    renderPage();
  }

  function applyTheme(theme) {
    document.documentElement.setAttribute("data-theme", theme);
    document.getElementById("theme").textContent = theme === "dark" ? t("Light") : t("Dark");
  }

  // Charts draw on canvas once, so their font has to be ready first; a slow
  // or blocked font must not hold the page.
  function fontsReady() {
    if (!document.fonts || !document.fonts.load) return Promise.resolve();
    var loaded = document.fonts.load('11px "Inter Variable"', "0Я").catch(function () { /* fallback font */ });
    return Promise.race([loaded, new Promise(function (done) { setTimeout(done, 1200); })]);
  }

  // showHealth says what is out of date, by series: one old series must not
  // read as a broken source.
  function showHealth() {
    load(["status"]).then(function (st) {
      // Hand input nobody has entered yet (manual) waits, it has not failed.
      var bad = st.sources.filter(function (s) { return s.errors || (s.never && s.source !== "manual"); });
      var stale = st.sources.filter(function (s) { return s.stale || (s.never && s.source === "manual"); });
      var level = bad.length ? "critical" : stale.length ? "warning" : "good";
      var text = t("All sources OK");
      if (bad.length) text = t("Source errors: {0}", bad.map(function (s) { return s.source.toUpperCase(); }).join(", "));
      else if (stale.length) text = stale.map(function (s) {
        return s.source.toUpperCase() + (s.stale ? t(": {0} of {1} series stale", s.stale, s.series) : t(": {0} of {1} series not entered", s.never, s.series));
      }).join("; ");
      var box = document.getElementById("health");
      box.replaceChildren(h("span", { class: "dot " + level }), text);
      box.hidden = false;
    }).catch(function () { /* the footer stays without the line */ });
  }

  function start() {
    state.range = get("macro.range", "3");
    var theme = get("macro.theme", "dark") === "light" ? "light" : "dark";
    applyTheme(theme);
    document.getElementById("theme").addEventListener("click", function () {
      theme = theme === "dark" ? "light" : "dark";
      put("macro.theme", theme);
      applyTheme(theme);
      if (state.page) renderPage();
    });
    var burger = document.getElementById("burger");
    burger.addEventListener("click", function () { setMenuOpen(burger.getAttribute("aria-expanded") !== "true"); });
    burger.setAttribute("aria-label", t("Menu"));
    document.documentElement.style.setProperty("--loading-text", JSON.stringify(t("Loading…")));
    document.getElementById("menu").setAttribute("aria-label", t("Pages"));
    document.getElementById("region-label").textContent = t("Region");
    var langButton = document.getElementById("lang");
    var other = I18N.lang() === "ru" ? "en" : "ru";
    langButton.textContent = other.toUpperCase();
    langButton.setAttribute("aria-label", t("Switch language"));
    langButton.addEventListener("click", function () { I18N.setLang(other); });
    var fitting = 0;
    window.addEventListener("resize", function () {
      if (fitting || !state.region) return;
      fitting = requestAnimationFrame(function () { fitting = 0; fitMenu(); });
    });
    Promise.all([load(["meta"]), fontsReady()]).then(function (ready) {
      var meta = localize(ready[0]);
      state.meta = meta;
      if (meta.demo) {
        // A page with everything closed is left out of the demo altogether.
        meta.regions.forEach(function (r) {
          r.pages = r.pages.filter(function (p) {
            return p.charts.concat(p.views).some(function (x) { return !x.hidden; });
          });
        });
      }
      state.regionPick = select({
        label: t("Region"), value: meta.regions[0].id,
        options: meta.regions.map(function (r) { return [r.id, r.id.toUpperCase()]; }),
        onChange: function (v) { location.hash = "#/" + v; },
      });
      var slot = document.getElementById("region");
      slot.appendChild(state.regionPick.el);
      slot.parentElement.hidden = meta.regions.length < 2;
      if (meta.demo || isStatic) {
        document.getElementById("foot").textContent = (meta.demo ? t("Demo on open data. Sources: {0}.", "FRED (Federal Reserve Bank of St. Louis), Federal Reserve Board, FRB New York (ON RRP, SOFR), BLS, BEA, U.S. Treasury, SEC EDGAR" + euSources(meta)) + " " : "") +
          t("Data as of {0}.", meta.generated_at.slice(0, 10)) + " This product uses the FRED® API but is not endorsed or certified by the Federal Reserve Bank of St. Louis.";
      }
      if (!meta.demo) showHealth();
      window.addEventListener("hashchange", route);
      route();
    }).catch(function (err) {
      if (window.console) console.error(err);
      document.getElementById("page").appendChild(h("div", { class: "card err" }, t("Failed to load the page list.")));
    });
  }

  return {
    start: start, load: load, series: series, report: report, h: h, fmt: fmt, q: q, num: num, draw: draw, plaque: plaque, change: change, timeChart: timeChart, follow: follow, select: select, inspect: inspect, countUp: countUp, expandButton: expandButton,
    t: t, locale: I18N.locale, lang: I18N.lang, pick: I18N.pick, plural: I18N.plural,
    css: css, axisStyle: axisStyle, axisLabel: axisLabel, tooltipValue: tooltipValue, fromDate: fromDate, state: state, enc: enc, views: {}, viewTitles: {},
  };
})();
