// Dashboard views: tables and blocks over the report JSON (D18). Each view
// fills its slot; every text goes through Macro.h, which never parses HTML.
(function () {
  "use strict";
  var M = window.Macro, h = M.h, q = M.q, fmt = M.fmt;
  var views = M.views;

  M.viewTitles = {
    key_metrics: "Ключевые показатели", status: "Состояние источников", fed_path: "Траектория ставки (ZQ)",
    auctions: "Аукционы Treasury", supply: "Чистое размещение", capex: "Capex", financials: "Финансы компаний",
    reactions: "Реакции на отчёты", issuers: "Компании", event_reactions: "Реакции рынка на события", news_feed: "Новости", surprises: "Сюрпризы", topics: "Темы", calendar: "Календарь", reports: "Отчёты",
  };

  function card(title) {
    var kids = [].slice.call(arguments, 1);
    return h("section", { class: "card" }, title ? h("h2", null, title) : null, kids);
  }
  // pressable is what a click and the keyboard open.
  function pressable(open) {
    return {
      tabindex: "0", onclick: open,
      onkeydown: function (e) { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); open(); } },
    };
  }
  // table draws rows; onRow(i), if set, makes each row open something.
  function table(head, rows, onRow) {
    return h("div", { class: "scroll" }, h("table", null,
      h("thead", null, h("tr", null, head.map(function (c) { return h("th", null, c); }))),
      h("tbody", null, rows.map(function (r, i) {
        var props = onRow ? Object.assign({ class: "row-link" }, pressable(function () { onRow(i); })) : null;
        return h("tr", props, r.map(function (c) { return c && c.nodeName === "TD" ? c : h("td", null, c === undefined || c === null ? "—" : c); }));
      }))));
  }
  function wrap(text) { return h("td", { class: "wrap text" }, text); }
  function empty(title) { return card(title, h("p", { class: "muted" }, "Нет данных за последнюю неделю.")); }
  function current(slot, route, node) { if (route === M.state.route) slot.replaceChildren(node); }
  function safeLink(url, text) {
    return /^https?:\/\//i.test(url || "") ? h("a", { href: url, target: "_blank", rel: "noopener noreferrer" }, text) : text;
  }

  // spark draws a level series as inline SVG: a dozen ECharts instances on one
  // phone page would be heavy.
  function spark(points, hover) {
    var ns = "http://www.w3.org/2000/svg";
    var svg = document.createElementNS(ns, "svg");
    svg.setAttribute("class", "spark");
    svg.setAttribute("viewBox", "0 0 100 30");
    svg.setAttribute("preserveAspectRatio", "none");
    svg.setAttribute("aria-hidden", "true");
    if (points.length < 2) return svg;
    var ys = points.map(function (p) { return p[1]; });
    var lo = Math.min.apply(null, ys), hi = Math.max.apply(null, ys), span = hi - lo || 1;
    var d = points.map(function (p, i) {
      return (i ? "L" : "M") + (i / (points.length - 1) * 100).toFixed(2) + " " + (27 - (p[1] - lo) / span * 24).toFixed(2);
    }).join("");
    var area = document.createElementNS(ns, "path");
    area.setAttribute("class", "area");
    area.setAttribute("d", d + "L100 30L0 30Z");
    var path = document.createElementNS(ns, "path");
    path.setAttribute("class", "line");
    path.setAttribute("d", d);
    svg.appendChild(area);
    svg.appendChild(path);
    if (hover) {
      var cursor = document.createElementNS(ns, "line");
      cursor.setAttribute("class", "cursor");
      cursor.setAttribute("y1", "0");
      cursor.setAttribute("y2", "30");
      svg.addEventListener("pointermove", function (e) {
        var r = svg.getBoundingClientRect();
        var i = Math.max(0, Math.min(points.length - 1, Math.round((e.clientX - r.left) / r.width * (points.length - 1))));
        var x = (i / (points.length - 1) * 100).toFixed(2);
        cursor.setAttribute("x1", x);
        cursor.setAttribute("x2", x);
        if (!cursor.parentNode) svg.appendChild(cursor);
        hover(points[i]);
      });
      svg.addEventListener("pointerleave", function () { cursor.remove(); hover(null); });
    }
    return svg;
  }

  // bar is a table cell with the value over a bar scaled to the column.
  function bar(text, value, max) {
    var share = max > 0 && value > 0 ? Math.min(value / max * 100, 100) : 0;
    return h("td", { class: "barcell" }, h("span", { class: "fill", style: "width:" + share.toFixed(1) + "%" }), h("span", { class: "barval" }, text));
  }
  function maxOf(items, value) {
    return items.reduce(function (m, x) { var v = value(x); return v > m ? v : m; }, 0);
  }
  function chg(o) { return o && o.value !== null && o.value !== undefined ? M.change(q(o, true), o.value) : "—"; }

  var SIDE_TILES = 3; // the rows next to the main chart of the overview

  function delta(label, o) { return h("span", null, h("em", null, label), chg(o)); }
  function staleMark(row) { return row.flags.indexOf("stale") >= 0 ? h("span", { class: "flag" }, " устарел") : null; }
  function refOf(key) {
    if (!key || !key.ref || key.hidden) return null;
    var cut = key.ref.indexOf("/");
    return { source: key.ref.slice(0, cut), id: key.ref.slice(cut + 1), transform: key.transform || "level" };
  }
  // sparkInto draws the last year; under the pointer the row shows that day.
  function sparkInto(box, key, valueEl, row) {
    var ref = refOf(key);
    if (!ref) return;
    M.series(ref.source, ref.id, ref.transform, M.fromDate("1")).then(function (s) {
      var dateEl = h("span", { class: "hoverdate" });
      box.replaceChildren(dateEl, spark(s.points, function (p) {
        valueEl.textContent = p ? fmt(p[1], s.unit) : q(row.value);
        dateEl.textContent = p ? p[0] : "";
      }));
    }).catch(function () { /* stays without a sparkline */ });
  }
  function openRow(row, key) {
    var ref = refOf(key);
    var facts = [["Значение в отчёте", q(row.value)], ["За неделю", chg(row.change_1w)], ["За 4 недели", chg(row.change_4w)]];
    if (row.z && row.z.value !== null && row.z.value !== undefined) facts.push(["Z-оценка", q(row.z)]);
    if (row.date) facts.push(["Дата", row.date]);
    M.inspect({ title: row.label, kind: "line", facts: facts, series: ref ? [Object.assign({ label: row.label }, ref)] : [] });
  }

  views.key_metrics = function (slot, v, region, route) {
    return M.report(region.id, v.agent, "key_metrics").then(function (rep) {
      if (!rep || !rep.rows.length) return current(slot, route, empty(M.viewTitles.key_metrics));
      var byLabel = {};
      region.key_rows.forEach(function (k) { byLabel[k.label] = k; });
      var lead = rep.rows[0], leadKey = byLabel[lead.label];
      var chartEl = h("div", { class: "chart hero-chart loading" });
      var side = rep.rows.slice(1, 1 + SIDE_TILES).map(function (row) {
        var box = h("div", { class: "sparkbox" });
        var value = h("div", { class: "value" }, q(row.value));
        sparkInto(box, byLabel[row.label], value, row);
        M.countUp(value, row.value && row.value.value, row.value && row.value.unit);
        return h("article", Object.assign({ class: "tile tile-link" }, pressable(function () { openRow(row, byLabel[row.label]); })),
          h("div", { class: "label" }, row.label, staleMark(row)),
          value,
          h("div", { class: "deltas" }, delta("неделя", row.change_1w), delta("4 недели", row.change_4w)),
          box);
      });
      var leadValue = h("span", { class: "now" }, q(lead.value));
      var leadDate = h("span", { class: "asof" }, lead.date ? "на " + lead.date : "");
      M.countUp(leadValue, lead.value && lead.value.value, lead.value && lead.value.unit);
      var hero = h("section", { class: "hero" },
        h("article", { class: "hero-main" },
          h("header", { class: "panel-head" }, h("div", null,
            h("h2", null, lead.label, staleMark(lead)),
            h("div", { class: "readout big" }, leadValue,
              h("div", { class: "deltas" }, delta("неделя", lead.change_1w), delta("4 недели", lead.change_4w)))),
            h("div", { class: "head-right" }, leadDate, M.expandButton(function () { openRow(lead, leadKey); }, lead.label))),
          chartEl),
        h("div", { class: "hero-side" }, side));
      var restRows = rep.rows.slice(1 + SIDE_TILES);
      var rest = restRows.map(function (row) {
        var box = h("div", { class: "sparkbox" });
        var value = h("td", null, q(row.value));
        sparkInto(box, byLabel[row.label], value, row);
        return [h("td", { class: "text" }, row.label, staleMark(row)), value, chg(row.change_1w), chg(row.change_4w), h("td", null, box)];
      });
      var watch = rest.length ? h("div", { class: "watch" }, card("Остальные показатели",
        table(["Показатель", "Значение", "Неделя", "4 недели", "Год"], rest, function (i) { openRow(restRows[i], byLabel[restRows[i].label]); }))) : null;
      var hidden = region.key_rows.filter(function (k) { return k.hidden; }).map(function (k) { return k.label; });
      var node = h("div", null, hero, watch, hidden.length ? M.plaque(hidden.join(", ") + ": скрыто в демо-версии (требуется лицензия источника)") : null);
      current(slot, route, node);
      var ref = refOf(leadKey);
      if (!ref) { chartEl.remove(); return; }
      M.series(ref.source, ref.id, ref.transform, M.fromDate("3")).then(function (s) {
        if (route !== M.state.route) return;
        // Up to the date of the report, so the headline and the chart agree.
        var points = s.points.filter(function (p) { return !lead.date || p[0] <= lead.date; });
        if (!points.length) { chartEl.remove(); return; }
        var chart = M.timeChart(chartEl, "line", [{ name: lead.label, unit: s.unit, points: points }]);
        M.follow(chart, function (date) {
          var p = date ? points.filter(function (x) { return x[0] <= date; }).pop() : null;
          leadValue.textContent = p ? fmt(p[1], s.unit) : q(lead.value);
          leadDate.textContent = "на " + (p ? p[0] : lead.date);
        });
      }).catch(function () { chartEl.remove(); });
    });
  };

  function when(t) { return t && t.indexOf("0001") !== 0 ? t.slice(0, 16).replace("T", " ") : "—"; }

  views.status = function (slot, v, region, route) {
    return M.load(["status"]).then(function (st) {
      var rows = st.sources.map(function (s) {
        // Hand input (Ifo) that nobody has entered yet is not a failed
        // source: it waits for macroctl input.
        var manual = s.source === "manual";
        var level = s.errors || (s.never && !manual) ? "critical" : s.stale || s.never ? "warning" : "good";
        var word = { critical: "ошибки", warning: manual && s.never && !s.stale ? "не введено" : "устарели", good: "норма" }[level];
        return [h("td", { class: "text" }, h("span", { class: "dot " + level }), s.source + " · " + word), s.series, s.errors, s.stale, s.never, when(s.last_ok)];
      });
      var problems = st.problems.slice(0, 20).map(function (p) {
        return [p.source + "/" + p.id, p.last_obs.indexOf("0001") === 0 ? "—" : p.last_obs.slice(0, 10), p.stale ? "устарел" : "", wrap(p.error || "")];
      });
      current(slot, route, h("div", null,
        card(M.viewTitles.status, table(["Источник", "Рядов", "Ошибки", "Устарели", "Нет данных", "Последний успех"], rows)),
        problems.length ? card("Ряды с проблемами (" + st.problems.length + ")", table(["Ряд", "Последнее наблюдение", "Флаг", "Ошибка"], problems)) : null));
    });
  };

  // chance mirrors derived.MoveProbability: the share of one 25 b.p. move.
  function chance(bp) {
    if (!bp || bp.value === null) return "—";
    var shown = Math.round(bp.value);
    var p = Math.min(Math.abs(shown) / 25, 1) * 100;
    if (Math.round(p) === 0) return "0%";
    return (shown < 0 ? "снижение " : "повышение ") + M.num(p, 0) + "%";
  }
  views.fed_path = function (slot, v, region, route) {
    return M.report(region.id, v.agent, "monetary").then(function (rep) {
      if (!rep || !rep.fed_path || !rep.fed_path.rows) return current(slot, route, empty(M.viewTitles.fed_path));
      var rows = rep.fed_path.rows.map(function (r) {
        return [r.month.slice(0, 7), r.meeting ? r.meeting.slice(0, 10) : "—", q(r.implied), q(r.vs_dff, true), q(r.meeting_bp, true), chance(r.meeting_bp), q(r.week_ago)];
      });
      current(slot, route, card(M.viewTitles.fed_path, table(["Контракт", "Заседание", "Implied", "К DFF", "За заседание", "Вероятность шага", "Неделю назад"], rows)));
    });
  };

  views.supply = function (slot, v, region, route) {
    return M.report(region.id, v.agent, "fiscal_liquidity").then(function (rep) {
      var s = rep && rep.supply;
      if (!s || !s.groups) return current(slot, route, empty(M.viewTitles.supply));
      var rows = s.groups.map(function (g) {
        return [g.name].concat(g.net.map(function (x) { return q(x, true); }));
      });
      current(slot, route, card(M.viewTitles.supply + " (чистое, по данным DTS на " + s.last_dts + ")",
        table(["Группа", "За неделю", "Квартал с " + s.quarter_start, "Прошлый квартал"], rows)));
    });
  };

  views.auctions = function (slot, v, region, route) {
    return M.report(region.id, v.agent, "fiscal_liquidity").then(function (rep) {
      if (!rep) return current(slot, route, empty(M.viewTitles.auctions));
      var top = maxOf(rep.coupons || [], function (a) { return a.amount ? a.amount.value : 0; });
      var coupons = (rep.coupons || []).map(function (a) {
        return [a.date, a.tenor + (a.reopening ? " (reopen)" : ""), bar(q(a.amount), a.amount && a.amount.value, top), q(a.high_yield), a.wait_cmt ? "ждём CMT" : q(a.tail_bp, true), q(a.btc), q(a.indirect), q(a.direct)];
      });
      var bills = (rep.bills || []).map(function (a) {
        return [a.name, a.count, q(a.amount), q(a.high_rate), q(a.btc)];
      });
      current(slot, route, h("div", null,
        coupons.length ? card("Купонные аукционы", table(["Дата", "Срок", "Объём", "Доходность", "Хвост", "Bid/cover", "Indirect", "Direct"], coupons)) : empty("Купонные аукционы"),
        bills.length ? card("Векселя", table(["Выпуск", "Аукционов", "Объём", "Ставка", "Bid/cover"], bills)) : null));
    });
  };

  views.capex = function (slot, v, region, route) {
    return M.report(region.id, v.agent, "micro").then(function (rep) {
      var c = rep && rep.capex;
      if (!c || !c.has_data) return current(slot, route, empty(M.viewTitles.capex));
      var el = h("div", { class: "chart tall" });
      var latest = function (r) { var x = r.values[r.values.length - 1]; return x ? x.value : 0; };
      var top = maxOf(c.rows, latest);
      var rows = c.rows.map(function (r) {
        var cells = r.values.map(function (x) { return q(x); });
        if (cells.length) cells[cells.length - 1] = bar(cells[cells.length - 1], latest(r), top);
        return [r.ticker].concat(cells, [q(r.qoq, true), q(r.yoy, true)]);
      });
      var node = h("div", null, card("Capex по кварталам, млрд $", el,
        h("p", { class: "note" }, "Сумма столбца — только компании, уже отчитавшиеся за квартал.")),
        card(null, table(["Компания"].concat(c.quarters, ["QoQ", "YoY"]), rows)));
      current(slot, route, node);
      if (route !== M.state.route) return;
      M.draw(el, {
        grid: { left: 8, right: 14, top: 34, bottom: 8, containLabel: true },
        tooltip: { trigger: "axis", valueFormatter: M.tooltipValue("usd_bn") },
        legend: { top: 0, left: 0 },
        xAxis: Object.assign({ type: "category", data: c.quarters }, M.axisStyle(), { splitLine: { show: false } }),
        yAxis: Object.assign({ type: "value" }, M.axisStyle()),
        series: c.rows.map(function (r) {
          return { name: r.ticker, type: "bar", stack: "capex", barMaxWidth: 44, data: r.values.map(function (x) { return x.value; }),
            itemStyle: { borderColor: M.css("--plane"), borderWidth: 1 } };
        }),
      });
    });
  };

  views.financials = function (slot, v, region, route) {
    return M.report(region.id, v.agent, "micro").then(function (rep) {
      var f = rep && rep.financials;
      if (!f) return current(slot, route, empty(M.viewTitles.financials));
      var rows = (f.rows || []).filter(function (r) { return r.has_data; }).map(function (r) {
        return [r.ticker, r.quarter, q(r.revenue), q(r.revenue_yoy, true), q(r.operating_margin), q(r.capex), q(r.fcf), q(r.fcf_ttm), q(r.net_debt)];
      });
      var rpo = (f.rpo || []).map(function (r) { return [r.ticker, r.date, q(r.rpo), q(r.rpo_yoy, true)]; });
      current(slot, route, h("div", null,
        card(M.viewTitles.financials, table(["Компания", "Квартал", "Выручка", "YoY", "Опер. маржа", "Capex", "FCF", "FCF TTM", "Чистый долг"], rows)),
        rpo.length ? card("RPO (контрактные обязательства)", table(["Компания", "Дата", "RPO", "YoY"], rpo)) : null));
    });
  };

  // issuers is EU Micro (report micro_annual): prices, reports of the week
  // with the reaction, next report dates, annual and quarterly numbers,
  // press releases. Issuer data is closed in the demo.
  views.issuers = function (slot, v, region, route) {
    return M.report(region.id, v.agent, "micro_annual").then(function (rep) {
      if (!rep) return current(slot, route, empty(M.viewTitles.issuers));
      var reaction = function (r) {
        if (r.pct && r.pct.value !== null) return q(r.pct, true) + " (" + r.date.slice(5) + ")";
        return r.waiting_close ? "ждём close " + r.waiting_close.slice(5) : "—";
      };
      var cap = function (r) { return r.pct && r.pct.value !== null && r.cap_change && r.cap_change.value !== null ? "≈ " + q(r.cap_change, true) : "—"; };
      var when = function (r) { return r.date ? r.date + (r.session === "after_close" ? ", после закрытия" : ", до открытия") + (r.estimate ? " (оценка)" : "") : "нет даты"; };
      var parts = [];
      if ((rep.reports || []).length) {
        parts.push(card("Отчёты недели", table(["Компания", "Отчёт", "Период", "Реакция цены", "Δ кап."],
          rep.reports.map(function (r) { return [r.name, when(r), r.period || "—", reaction(r.reaction), cap(r.reaction)]; }))));
      }
      parts.push(card("Цены", table(["Компания", "Дата", "Закрытие", "Δ1w", "Δ4w", "Δ1w к " + rep.index_label + ", п.п."],
        (rep.prices || []).map(function (p) { return [p.name, p.date || "—", q(p.close), q(p.change_1w, true), q(p.change_4w, true), q(p.vs_index, true)]; }))));
      parts.push(card("Ближайшие отчёты", table(["Компания", "Дата", "Период", "Источник даты"],
        (rep.upcoming || []).map(function (r) { return [r.name, when(r), r.period || "—", r.source || "—"]; }))));
      var withYoY = function (v, yoy) { return v && v.value !== null ? q(v) + (yoy && yoy.value !== null ? " (" + q(yoy, true) + ")" : "") : "—"; };
      if ((rep.annual || []).length) {
        parts.push(card("Годовые цифры", table(["Компания", "Год", "Выручка", "Опер. прибыль", "Чистая прибыль", "EPS", "FCF", "Источник"],
          rep.annual.map(function (a) {
            return [a.name, String(a.year) + (a.stale ? " ⚠" : ""), withYoY(a.revenue, a.revenue_yoy), withYoY(a.operating_income, a.operating_yoy),
              withYoY(a.net_income, a.net_yoy), withYoY(a.eps, a.eps_yoy), withYoY(a.fcf, a.fcf_yoy), a.source];
          }))));
      }
      if ((rep.quarterly || []).length) {
        parts.push(card("Квартальные цифры (Excel эмитента)", table(["Компания", "Квартал", "Выручка", "Опер. прибыль (показатель компании)", "Чистая прибыль", "EPS"],
          rep.quarterly.map(function (r) {
            return [r.name, r.quarter, withYoY(r.revenue, r.revenue_yoy), withYoY(r.operating, r.operating_yoy) + " — " + r.operating_label,
              withYoY(r.net_income, r.net_yoy), withYoY(r.eps, r.eps_yoy)];
          }))));
      }
      if ((rep.releases || []).length) {
        parts.push(card("Пресс-релизы недели", table(["Дата", "Компания", "Релиз"],
          rep.releases.map(function (r) { return [r.published, r.name, h("td", { class: "wrap text" }, safeLink(r.url, r.title))]; }))));
      }
      current(slot, route, h("div", null, parts));
    });
  };

  views.reactions = function (slot, v, region, route) {
    var week = "latest";
    function paint() {
      return M.load(["reports", region.id, v.agent, week]).then(function (r) {
        var f = r.files.filter(function (x) { return x.report === "micro"; })[0];
        var rep = f && f.json;
        var list = (rep && rep.week) || [];
        var picks = M.select({ label: "Неделя", value: r.week, options: r.weeks.map(function (w) { return [w, w]; }), onChange: function (w) { week = w; paint(); } });
        var controls = h("div", { class: "controls" }, picks.el);
        if (!list.length) return current(slot, route, h("div", null, controls, card(M.viewTitles.reactions, h("p", { class: "muted" }, "За эту неделю компании не отчитывались."))));
        var known = list.filter(function (x) { return x.reaction && !x.reaction.waiting_close && x.reaction.pct && x.reaction.pct.value !== null; })
          .sort(function (a, b) { return b.reaction.pct.value - a.reaction.pct.value; });
        var tiles = null;
        if (known.length) {
          var mean = known.reduce(function (m, x) { return m + x.reaction.pct.value; }, 0) / known.length;
          var unit = known[0].reaction.pct.unit, best = known[0], worst = known[known.length - 1];
          var tile = function (label, value, sub) { return h("article", { class: "tile" }, h("div", { class: "label" }, label), h("div", { class: "value" }, M.change(fmt(value, unit, true), value)), h("div", { class: "muted" }, sub)); };
          // Russian plural: 1 компания, 2-4 компании, 5+ компаний (11-14 are "компаний").
          var n = known.length, d = n % 10, dd = n % 100;
          var word = d === 1 && dd !== 11 ? "компания" : d >= 2 && d <= 4 && (dd < 12 || dd > 14) ? "компании" : "компаний";
          // With one report there is nothing to average or rank: show it once.
          tiles = n === 1
            ? h("div", { class: "tiles" }, tile("Реакция на отчёт", best.reaction.pct.value, best.ticker))
            : h("div", { class: "tiles" },
              tile("Средняя реакция", mean, n + " " + word),
              tile("Лучшая реакция", best.reaction.pct.value, best.ticker),
              tile("Худшая реакция", worst.reaction.pct.value, worst.ticker));
        }
        var rows = list.map(function (x) {
          var re = x.reaction || {};
          return [x.ticker, x.quarter, x.report_at ? x.report_at.slice(0, 10) : "—", chg(x.revenue_yoy), q(x.eps), re.waiting_close ? "ждём закрытия" : chg(re.pct), chg(re.cap_change)];
        });
        current(slot, route, h("div", null, controls, tiles, card("Реакция цены на отчётность",
          table(["Компания", "Квартал", "Отчёт", "Выручка YoY", "EPS", "Реакция цены", "Δ капитализации"], rows))));
      });
    }
    return paint();
  };

  // moved is "30 мин" over "до закрытия": a percent, or ≈ b.p. for a yield.
  function moved(m) {
    if (!m) return h("td", null, "—");
    return h("td", { class: "two" }, h("div", null, chg(m.first)), h("div", { class: "chg-sub" }, chg(m.full)));
  }
  views.event_reactions = function (slot, v, region, route) {
    return M.report(region.id, v.agent, "sentiment").then(function (rep) {
      var r = rep && rep.reaction;
      if (!r || !r.rows || !r.rows.length) return current(slot, route, empty(M.viewTitles.event_reactions));
      var rows = r.rows.map(function (e) {
        var surprises = (e.surprises || []).map(function (x) { return h("div", { class: "surprise" }, x.label + ": " + x.actual + " при ожидании " + x.expected); });
        return [h("td", { class: "wrap text" }, h("div", { class: "ev-name" }, e.labels.join(", ")), surprises),
          e.at.slice(0, 16).replace("T", " ")].concat(e.moves.map(moved));
      });
      var parts = [card(M.viewTitles.event_reactions,
        h("p", { class: "muted lead" }, "Верхнее значение — первые " + r.window_minutes + " минут после события, нижнее — до закрытия. Время — ET."),
        table(["Событие", "Время"].concat(r.symbols), rows))];
      var w = rep.moves;
      if (w && w.rows && w.rows.length) {
        var dayRows = w.rows.map(function (row) {
          return [row.label].concat(row.values.map(function (x, i) {
            return h("td", { class: w.days[i] === row.best ? "best" : "" }, chg(x));
          }));
        });
        parts.push(card("Движение по дням недели", h("p", { class: "muted lead" }, "Выделен день с наибольшим движением."),
          table(["Инструмент"].concat(w.days.map(function (d) { return d.slice(5); })), dayRows)));
      }
      current(slot, route, h("div", null, parts));
    });
  };

  views.surprises = function (slot, v, region, route) {
    return M.report(region.id, v.agent, "news").then(function (rep) {
      if (!rep || !rep.surprises || !rep.surprises.length) return current(slot, route, empty(M.viewTitles.surprises));
      var rows = rep.surprises.map(function (s) {
        return [s.date, h("td", { class: "wrap text" }, safeLink(s.url, s.label)), s.expected, s.actual, s.diff];
      });
      current(slot, route, card(M.viewTitles.surprises, table(["Дата", "Показатель", "Ожидание", "Факт", "Разница"], rows)));
    });
  };

  views.topics = function (slot, v, region, route) {
    return M.report(region.id, v.agent, "news").then(function (rep) {
      var topics = rep && rep.mentions && rep.mentions.topics;
      if (!topics || !topics.length) return current(slot, route, empty(M.viewTitles.topics));
      var sorted = topics.slice().sort(function (a, b) { return a.now - b.now; });
      var el = h("div", { class: "chart tall" });
      // "now" is the report's week, "before" the week before it.
      var quiet = topics.every(function (t) { return !t.now; });
      current(slot, route, h("div", null, card(M.viewTitles.topics + ": упоминания за неделю", h("div", null,
        quiet ? h("p", { class: "muted" }, "За эту неделю постов с этими темами нет — видна только прошлая неделя.") : null, el)),
        card(null, table(["Тема", "Эта неделя", "Прошлая неделя"], sorted.slice().reverse().map(function (t) { return [t.name, t.now, t.before]; })))));
      if (route !== M.state.route) return;
      M.draw(el, {
        grid: { left: 8, right: 14, top: 34, bottom: 8, containLabel: true },
        tooltip: { trigger: "axis", axisPointer: { type: "shadow" } },
        legend: { top: 0, left: 0 },
        yAxis: Object.assign({ type: "category", data: sorted.map(function (t) { return t.name; }) }, M.axisStyle(), { splitLine: { show: false } }),
        xAxis: Object.assign({ type: "value", minInterval: 1 }, M.axisStyle()),
        series: [
          { name: "Эта неделя", type: "bar", barMaxWidth: 10, data: sorted.map(function (t) { return t.now; }) },
          { name: "Прошлая неделя", type: "bar", barMaxWidth: 10, data: sorted.map(function (t) { return t.before; }) },
        ],
      });
    });
  };

  var IMPORTANCE = [["high", "Высокая"], ["medium", "Средняя"], ["low", "Низкая"]];
  function startOfDay(d) { return new Date(d.getFullYear(), d.getMonth(), d.getDate()); }
  function addDays(d, n) { return new Date(d.getFullYear(), d.getMonth(), d.getDate() + n); }
  function monday(d) { return addDays(d, -((d.getDay() + 6) % 7)); }
  function dayText(d, opts) { return d.toLocaleDateString("ru-RU", opts); }

  views.calendar = function (slot, v, region, route) {
    return M.load(["calendar"], "days=60").then(function (events) {
      var mode = "week", day = startOfDay(new Date()), on = { high: true, medium: true, low: true };
      var controls = h("div", { class: "cal-bar" }), list = h("div");
      function span() {
        var from = mode === "day" ? day : monday(day);
        return [from, addDays(from, mode === "day" ? 1 : 7)];
      }
      function move(n) { day = addDays(day, n * (mode === "day" ? 1 : 7)); paint(); }
      function paint() {
        var range = span(), from = range[0], to = range[1];
        var title = mode === "day"
          ? dayText(from, { day: "numeric", month: "long", year: "numeric" })
          : dayText(from, { day: "numeric", month: "short" }) + " — " + dayText(addDays(to, -1), { day: "numeric", month: "short", year: "numeric" });
        controls.replaceChildren(
          h("div", { class: "filters" }, [["day", "День"], ["week", "Неделя"]].map(function (m) {
            return h("button", { type: "button", class: mode === m[0] ? "on" : "", onclick: function () { mode = m[0]; paint(); } }, m[1]);
          })),
          h("div", { class: "cal-nav" },
            h("button", { type: "button", class: "round", "aria-label": "Назад", onclick: function () { move(-1); } }, "‹"),
            h("span", { class: "cal-date" }, title),
            h("button", { type: "button", class: "round", "aria-label": "Вперёд", onclick: function () { move(1); } }, "›")),
          h("button", { type: "button", class: "today", onclick: function () { day = startOfDay(new Date()); paint(); } }, "Сегодня"),
          h("div", { class: "imp-pills" }, IMPORTANCE.map(function (x) {
            return h("button", { type: "button", class: "imp-pill " + x[0], "aria-pressed": on[x[0]] ? "true" : "false", onclick: function () { on[x[0]] = !on[x[0]]; paint(); } }, h("i"), x[1]);
          })),
          h("span", { class: "muted cal-tz" }, "Время браузера"));
        var days = new Map();
        events.forEach(function (e) {
          var at = new Date(e.at), level = e.importance || "low";
          if (e.region !== region.id || !on[level] || at < from || at >= to) return;
          var key = dayText(at, { weekday: "long", day: "numeric", month: "long" });
          if (!days.has(key)) days.set(key, []);
          days.get(key).push(h("div", { class: "ev" },
            h("i", { class: "ev-dot " + level, title: level }),
            h("span", { class: "ev-time" }, at.toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" })),
            h("span", { class: "ev-name" }, e.name),
            at < new Date() ? h("span", { class: "ev-done" }, "вышло") : null));
        });
        var kids = [];
        days.forEach(function (rows, key) { kids.push(h("section", { class: "ev-day" }, h("h3", null, key), rows)); });
        list.replaceChildren.apply(list, kids.length ? kids : [h("p", { class: "muted ev-none" }, "В этот период событий нет.")]);
      }
      paint();
      current(slot, route, h("div", null, controls, h("div", { class: "card" }, list)));
    });
  };

  // firstLine is the headline of a post: its first non-empty line.
  function firstLine(text) {
    var line = (text || "").split("\n").map(function (x) { return x.trim(); }).filter(Boolean)[0] || "";
    return line.length > 170 ? line.slice(0, 167) + "…" : line;
  }
  views.news_feed = function (slot, v, region, route) {
    return M.report(region.id, v.agent, "news").then(function (rep) {
      var news = (rep && rep.news) || [], opinions = (rep && rep.opinions) || [];
      if (!news.length && !opinions.length) return current(slot, route, empty(M.viewTitles.news_feed));
      var kind = "news", topic = "";
      var bar = h("div", { class: "cal-bar" }), list = h("div");
      function groups() { return kind === "news" ? news : opinions; }
      function row(p) {
        var body = h("p", { class: "feed-text", hidden: true }, p.text);
        var hot = p.resonance && p.resonance.value >= 1.5;
        var el = h("article", { class: "feed-row", tabindex: "0",
          onclick: function (e) { if (e.target.tagName !== "A") body.hidden = !body.hidden; },
          onkeydown: function (e) { if (e.key === "Enter") body.hidden = !body.hidden; } },
          h("span", { class: "ev-time" }, new Date(p.published).toLocaleDateString("ru-RU", { day: "numeric", month: "short" })),
          h("div", { class: "feed-main" },
            h("div", { class: "feed-meta" }, safeLink(p.url, p.author), p.views ? " · " + M.num(p.views, 0) + " просм." : ""),
            h("div", { class: "feed-title" }, firstLine(p.text)), body),
          hot ? h("span", { class: "feed-tag" }, "Резонанс") : null);
        return el;
      }
      function paint() {
        var names = groups().map(function (g) { return g.title; });
        if (names.indexOf(topic) < 0) topic = "";
        bar.replaceChildren(
          h("div", { class: "filters" }, [["news", "Новости"], ["opinions", "Мнения"]].map(function (k) {
            return h("button", { type: "button", class: kind === k[0] ? "on" : "", onclick: function () { kind = k[0]; paint(); } }, k[1]);
          })),
          M.select({ label: "Тема", value: topic, options: [["", "Все темы"]].concat(names.map(function (n) { return [n, n]; })), onChange: function (t) { topic = t; paint(); } }).el);
        var posts = [];
        groups().forEach(function (g) { if (!topic || g.title === topic) posts = posts.concat(g.posts); });
        posts.sort(function (a, b) { return a.published < b.published ? 1 : -1; });
        list.replaceChildren.apply(list, posts.length ? posts.map(row) : [h("p", { class: "muted" }, "Постов нет.")]);
      }
      paint();
      current(slot, route, h("div", null, bar, h("div", { class: "card" }, list)));
    });
  };

  views.reports = function (slot, v, region, route) {
    var agent = region.agents[0], week = "latest";
    var body = h("div");
    function paint() {
      return M.load(["reports", region.id, agent, week]).then(function (r) {
        if (route !== M.state.route) return;
        var agents = M.select({ label: "Агент", value: agent, options: region.agents.map(function (a) { return [a, a]; }),
          onChange: function (v) { agent = v; week = "latest"; paint(); } });
        var weeks = M.select({ label: "Неделя", value: r.week, options: r.weeks.map(function (w) { return [w, w]; }),
          onChange: function (v) { week = v; paint(); } });
        var parts = [h("div", { class: "controls" }, agents.el, weeks.el)];
        if (!r.files.length) parts.push(h("p", { class: "muted" }, "Экспортов за эту неделю нет."));
        r.files.forEach(function (f) {
          var md = h("div", { class: "md" });
          md.innerHTML = window.MacroMarkdown.render(f.md || ""); // escaped by the renderer
          parts.push(card(f.file, md, f.json ? h("details", null, h("summary", null, "JSON"), h("pre", { class: "json" }, JSON.stringify(f.json, null, 2))) : null));
        });
        if (r.answer) {
          var ans = h("div", { class: "md" });
          ans.innerHTML = window.MacroMarkdown.render(r.answer); // escaped by the renderer
          parts.push(card("Ответ агента", ans));
        }
        body.replaceChildren.apply(body, parts);
      });
    }
    return paint().then(function () { current(slot, route, body); });
  };
})();
