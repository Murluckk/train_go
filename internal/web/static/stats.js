// Statistics: how long each task takes over time, and which topics are slow.
//
// The charts are hand drawn SVG. A charting library from a CDN would be a
// dependency for two chart types, and this way the page also renders offline.

import { el, mount, api, fmtShort, fmtDay, plural } from "/util.js";

export async function renderStats(root) {
  const [tasks, topics] = await Promise.all([
    api("/api/stats/tasks"),
    api("/api/stats/topics"),
  ]);

  mount(root, 
    el("h1", {}, "Статистика"),
    el("p", { class: "sub" },
      "Время считается по клавиатуре, а не по настенным часам: пауза на кофе в график не попадает."),
  );

  mount(root, el("h2", {}, "Где я медленнее всего"));
  const measured = (topics.topics || []).filter((t) => t.solves > 0);
  if (measured.length === 0) {
    mount(root, el("p", { class: "empty" }, "Ещё нечего мерить — реши хотя бы одну задачу."));
  } else {
    const worst = Math.max(...measured.map((t) => t.median_active_ms));
    mount(root, el("div", {}, measured.map((t) =>
      el("div", { class: "bar-row" },
        el("div", {}, t.topic,
          t.unsolved_tasks
            ? el("span", { class: "muted" }, ` · ${t.unsolved_tasks} не тронуто`)
            : null),
        el("div", { class: "bar-track" },
          el("div", { class: "bar-fill", style: `width:${Math.max(2, (t.median_active_ms / worst) * 100)}%` })),
        el("div", { class: "bar-value" },
          `${fmtShort(t.median_active_ms)} · ${plural(t.solves, "решение", "решения", "решений")}`),
      ),
    )));
    mount(root, el("p", { class: "sub" },
      "Столбик — медиана времени решения по теме. p90 и среднее число красных прогонов ниже."));
    mount(root, topicTable(measured));
  }

  mount(root, el("h2", {}, "Динамика по задачам"));
  const series = (tasks.series || []).filter((s) => s.points.length > 0);
  if (series.length === 0) {
    mount(root, el("p", { class: "empty" }, "Графики появятся после первых зелёных прогонов."));
    return;
  }

  // Regressions first: that is the actionable end of the list.
  series.sort((a, b) => b.trend_pct - a.trend_pct);
  for (const s of series) {
    mount(root, taskChart(s));
  }

  mount(root, el("h2", {}, "Активность"), activityStrip(tasks.activity || [], tasks.today));
}

function topicTable(topics) {
  return el("div", { class: "panel" },
    el("div", { class: "panel-head" }, "тема · медиана · p90 · красных прогонов на решение"),
    el("div", { class: "panel-body" },
      topics.map((t) =>
        el("div", { class: "bar-row" },
          el("div", {}, t.topic),
          el("div", { class: "muted" }, `p90 ${fmtShort(t.p90_active_ms)}`),
          el("div", { class: "bar-value" }, t.avg_failed_runs.toFixed(1)),
        ),
      ),
    ),
  );
}

function taskChart(s) {
  const W = 640;
  const H = 170;
  const padL = 46;
  const padR = 12;
  const padT = 12;
  const padB = 24;

  const values = s.points.map((p) => p.active_ms);
  const maxV = Math.max(...values);
  const minV = 0;
  const n = values.length;

  const x = (i) => padL + (n === 1 ? (W - padL - padR) / 2 : (i * (W - padL - padR)) / (n - 1));
  const y = (v) => padT + (1 - (v - minV) / (maxV - minV || 1)) * (H - padT - padB);

  const path = values.map((v, i) => `${i === 0 ? "M" : "L"}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join(" ");

  const svg = svgEl("svg", { class: "chart", viewBox: `0 0 ${W} ${H}`, preserveAspectRatio: "none" });

  for (const frac of [0, 0.5, 1]) {
    const gy = padT + frac * (H - padT - padB);
    svg.append(svgEl("line", { class: "grid", x1: padL, y1: gy, x2: W - padR, y2: gy }));
    svg.append(svgText(6, gy + 3, axisLabel(Math.round(maxV * (1 - frac)))));
  }

  svg.append(svgEl("path", { class: "line", d: path }));

  s.points.forEach((p, i) => {
    const regress = p.active_ms > s.best_ms * 1.25;
    const dot = svgEl("circle", { class: `dot${regress ? " regress" : ""}`, cx: x(i), cy: y(p.active_ms), r: 3.5 });
    dot.append(svgEl("title", {}, `${fmtDay(p.day)} · ${fmtShort(p.active_ms)} · красных прогонов ${p.fail_count}`));
    svg.append(dot);
  });

  svg.append(svgText(padL, H - 6, fmtDay(s.points[0].day)));
  if (n > 1) {
    const label = svgText(W - padR, H - 6, fmtDay(s.points[n - 1].day));
    label.setAttribute("text-anchor", "end");
    svg.append(label);
  }

  const trend = s.trend_pct === 0
    ? "как в лучший раз"
    : s.trend_pct > 0
      ? `на ${s.trend_pct}% медленнее лучшего`
      : `на ${-s.trend_pct}% быстрее прежнего лучшего`;

  return el("div", { class: "panel", style: "margin-bottom:14px" },
    el("div", { class: "panel-head" },
      el("a", { href: `#/task/${s.task_id}` }, s.title),
      el("span", { style: "flex:1" }),
      el("span", {}, `${s.topic} · лучшее ${fmtShort(s.best_ms)} · последнее ${fmtShort(s.last_ms)} · ${trend}`),
    ),
    el("div", { class: "panel-body" }, svg),
  );
}

function activityStrip(activity, today) {
  const byDay = new Map(activity.map((a) => [a.day, a.solves]));
  const cells = [];
  const end = today ? new Date(today + "T00:00:00") : new Date();

  for (let i = 90; i >= 0; i--) {
    const d = new Date(end);
    d.setDate(d.getDate() - i);
    const key = d.toISOString().slice(0, 10);
    const count = byDay.get(key) || 0;
    const shade = count === 0 ? 0 : Math.min(1, 0.35 + count * 0.22);
    cells.push(el("i", {
      class: "cell",
      title: `${fmtDay(key)}: ${plural(count, "решение", "решения", "решений")}`,
      style: shade
        ? `background: color-mix(in srgb, var(--accent) ${Math.round(shade * 100)}%, transparent)`
        : "",
    }));
  }
  return el("div", { class: "activity" }, cells);
}

// fmtShort renders 0 as an em dash, which is right in a table and wrong on an
// axis where zero is a real position.
function axisLabel(ms) {
  return ms === 0 ? "0" : fmtShort(ms);
}

function svgEl(name, attrs = {}, ...children) {
  const node = document.createElementNS("http://www.w3.org/2000/svg", name);
  for (const [k, v] of Object.entries(attrs)) node.setAttribute(k, String(v));
  for (const c of children) node.append(c instanceof Node ? c : document.createTextNode(String(c)));
  return node;
}

function svgText(x, y, text) {
  const node = svgEl("text", { x, y });
  node.textContent = text;
  return node;
}
