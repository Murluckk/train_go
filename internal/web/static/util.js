// Small helpers: no framework, no dependencies.

/** el builds a DOM node. Children may be nodes, strings, or nested arrays. */
export function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === null || v === undefined || v === false) continue;
    if (k === "class") node.className = v;
    else if (k === "html") node.innerHTML = v;
    else if (k.startsWith("on") && typeof v === "function") node.addEventListener(k.slice(2), v);
    else if (k === "dataset") Object.assign(node.dataset, v);
    else node.setAttribute(k, v === true ? "" : String(v));
  }
  append(node, children);
  return node;
}

/**
 * mount appends children to an existing node, flattening arrays and dropping
 * null and false. The native Node.append does neither: given an array it
 * stringifies it into "[object HTMLDivElement]", which is a quiet and very
 * confusing way to lose a whole section of the page.
 */
export function mount(node, ...children) {
  return append(node, children);
}

function append(node, children) {
  for (const child of children) {
    if (child === null || child === undefined || child === false) continue;
    if (Array.isArray(child)) append(node, child);
    else node.append(child instanceof Node ? child : document.createTextNode(String(child)));
  }
  return node;
}

export function clear(node) {
  node.replaceChildren();
  return node;
}

/** api calls the backend and turns a non-2xx into a thrown Error. */
export async function api(path, { method = "GET", body, signal } = {}) {
  const init = { method, signal, headers: {} };
  if (body !== undefined) {
    init.headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(body);
  }
  const resp = await fetch(path, init);
  if (resp.status === 204) return null;

  const text = await resp.text();
  let payload = null;
  if (text) {
    try {
      payload = JSON.parse(text);
    } catch {
      payload = { error: text };
    }
  }
  if (!resp.ok) {
    const err = new Error(payload?.error || `${method} ${path}: ${resp.status}`);
    err.status = resp.status;
    err.detail = payload?.detail;
    throw err;
  }
  return payload;
}

/** fmtClock renders milliseconds as mm:ss, or h:mm:ss past an hour. */
export function fmtClock(ms) {
  const total = Math.max(0, Math.round(ms / 1000));
  const s = total % 60;
  const m = Math.floor(total / 60) % 60;
  const h = Math.floor(total / 3600);
  const pad = (n) => String(n).padStart(2, "0");
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${pad(m)}:${pad(s)}`;
}

/** fmtShort renders a duration compactly for tables: "7м 12с". */
export function fmtShort(ms) {
  if (!ms) return "—";
  const total = Math.round(ms / 1000);
  if (total < 60) return `${total}с`;
  const m = Math.floor(total / 60);
  const s = total % 60;
  return s ? `${m}м ${s}с` : `${m}м`;
}

export function fmtDay(day) {
  if (!day) return "";
  const [y, m, d] = day.split("-");
  return `${d}.${m}.${y}`;
}

export function plural(n, one, few, many) {
  const mod10 = n % 10;
  const mod100 = n % 100;
  if (mod10 === 1 && mod100 !== 11) return `${n} ${one}`;
  if (mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14)) return `${n} ${few}`;
  return `${n} ${many}`;
}

export function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  })[c]);
}

/**
 * markdown renders the small subset the task statements use: headings, fenced
 * code, inline code, bold, links and lists. A full parser from a CDN would be
 * another dependency for a job this size.
 */
export function markdown(src) {
  const lines = String(src).replace(/\r\n/g, "\n").split("\n");
  const out = [];
  let code = null; // collected lines of the current fenced block
  let inList = false;
  let paragraph = [];

  const flushParagraph = () => {
    if (paragraph.length) {
      out.push(`<p>${inline(paragraph.join(" "))}</p>`);
      paragraph = [];
    }
  };
  const closeList = () => {
    if (inList) {
      out.push("</ul>");
      inList = false;
    }
  };

  for (const line of lines) {
    if (line.startsWith("```")) {
      if (code === null) {
        flushParagraph();
        closeList();
        code = [];
      } else {
        // One entry for the whole block: joining the lines here keeps the
        // blank lines inside a snippet from being doubled by the join below.
        out.push(`<pre><code>${escapeHTML(code.join("\n"))}</code></pre>`);
        code = null;
      }
      continue;
    }
    if (code !== null) {
      code.push(line);
      continue;
    }

    const heading = /^(#{1,4})\s+(.*)$/.exec(line);
    if (heading) {
      flushParagraph();
      closeList();
      const level = heading[1].length;
      out.push(`<h${level}>${inline(heading[2])}</h${level}>`);
      continue;
    }

    const item = /^[-*]\s+(.*)$/.exec(line);
    if (item) {
      flushParagraph();
      if (!inList) {
        out.push("<ul>");
        inList = true;
      }
      out.push(`<li>${inline(item[1])}</li>`);
      continue;
    }

    // An indented line right after a bullet continues that bullet rather
    // than starting a paragraph of its own.
    if (inList && paragraph.length === 0 && /^\s+\S/.test(line)) {
      out[out.length - 1] = out[out.length - 1].replace(/<\/li>$/, " " + inline(line.trim()) + "</li>");
      continue;
    }

    if (line.trim() === "") {
      flushParagraph();
      closeList();
      continue;
    }
    paragraph.push(line.trim());
  }

  flushParagraph();
  closeList();
  if (code !== null) out.push(`<pre><code>${escapeHTML(code.join("\n"))}</code></pre>`);
  return out.join("\n");
}

function inline(s) {
  return escapeHTML(s)
    .replace(/`([^`]+)`/g, "<code>$1</code>")
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
    .replace(/\[([^\]]+)\]\(([^)]+)\)/g, '<a href="$2" rel="noreferrer">$1</a>');
}

export function setStatus(text, kind = "") {
  const node = document.getElementById("status");
  if (!node) return;
  node.textContent = text;
  node.className = "status" + (kind ? " " + kind : "");
}

export function errorBox(err) {
  const detail = err?.detail ? ` (${err.detail})` : "";
  return el("div", { class: "error-box" }, (err?.message || String(err)) + detail);
}
