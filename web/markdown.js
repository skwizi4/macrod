// A small Markdown renderer for the export files. Everything is escaped
// first: the files carry third-party text (Telegram posts).
window.MacroMarkdown = (function () {
  "use strict";
  function escapeHTML(s) {
    return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&#39;");
  }
  function inline(text) {
    var codes = [];
    var s = escapeHTML(text).replace(/`([^`]+)`/g, function (_, c) {
      codes.push(c);
      return "\u0000" + (codes.length - 1) + "\u0000";
    });
    s = s.replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, function (all, label, url) {
      if (!/^https?:\/\//i.test(url)) return label;
      return '<a href="' + url + '" target="_blank" rel="noopener noreferrer">' + label + "</a>";
    });
    s = s.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>").replace(/(^|[\s(])\*([^*\s][^*]*)\*/g, "$1<em>$2</em>");
    return s.replace(/\u0000(\d+)\u0000/g, function (_, i) { return "<code>" + codes[+i] + "</code>"; });
  }
  function cells(line) {
    return line.trim().replace(/^\|/, "").replace(/\|$/, "").split("|").map(function (c) { return c.trim(); });
  }
  function render(src) {
    var lines = src.replace(/\r\n/g, "\n").split("\n");
    var out = [], i = 0;
    while (i < lines.length) {
      var line = lines[i];
      var m;
      if (/^```/.test(line)) {
        var code = [];
        for (i++; i < lines.length && !/^```/.test(lines[i]); i++) code.push(lines[i]);
        i++;
        out.push("<pre><code>" + escapeHTML(code.join("\n")) + "</code></pre>");
      } else if (i === 0 && line === "---") {
        for (i++; i < lines.length && lines[i] !== "---"; i++);
        i++;
      } else if ((m = /^(#{1,6})\s+(.*)$/.exec(line))) {
        out.push("<h" + m[1].length + ">" + inline(m[2]) + "</h" + m[1].length + ">");
        i++;
      } else if (/^\s*\|/.test(line) && i + 1 < lines.length && /^\s*\|?[\s:|-]+$/.test(lines[i + 1]) && lines[i + 1].indexOf("-") >= 0) {
        var head = cells(line).map(function (c) { return "<th>" + inline(c) + "</th>"; }).join("");
        var rows = [];
        for (i += 2; i < lines.length && /^\s*\|/.test(lines[i]); i++) {
          rows.push("<tr>" + cells(lines[i]).map(function (c) { return "<td>" + inline(c) + "</td>"; }).join("") + "</tr>");
        }
        out.push('<div class="scroll"><table><thead><tr>' + head + "</tr></thead><tbody>" + rows.join("") + "</tbody></table></div>");
      } else if (/^\s*[-*]\s+/.test(line)) {
        var items = [];
        for (; i < lines.length && /^\s*[-*]\s+/.test(lines[i]); i++) items.push("<li>" + inline(lines[i].replace(/^\s*[-*]\s+/, "")) + "</li>");
        out.push("<ul>" + items.join("") + "</ul>");
      } else if (/^\s*\d+\.\s+/.test(line)) {
        var numbered = [];
        for (; i < lines.length && /^\s*\d+\.\s+/.test(lines[i]); i++) numbered.push("<li>" + inline(lines[i].replace(/^\s*\d+\.\s+/, "")) + "</li>");
        out.push("<ol>" + numbered.join("") + "</ol>");
      } else if (/^>\s?/.test(line)) {
        var quote = [];
        for (; i < lines.length && /^>\s?/.test(lines[i]); i++) quote.push(inline(lines[i].replace(/^>\s?/, "")));
        out.push("<blockquote>" + quote.join("<br>") + "</blockquote>");
      } else if (/^\s*(-{3,}|\*{3,})\s*$/.test(line)) {
        out.push("<hr>");
        i++;
      } else if (line.trim() === "") {
        i++;
      } else {
        var para = [];
        for (; i < lines.length && lines[i].trim() !== "" && !/^(#{1,6}\s|```|>|\s*[-*]\s|\s*\||\s*\d+\.\s)/.test(lines[i]); i++) para.push(inline(lines[i]));
        if (para.length === 0) { para.push(inline(lines[i])); i++; }
        out.push("<p>" + para.join("<br>") + "</p>");
      }
    }
    return out.join("\n");
  }
  return { render: render };
})();
