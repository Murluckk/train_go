// Router and screens.

import { el, clear, mount, api, fmtShort, fmtDay, plural, setStatus, errorBox } from "/util.js";
import { renderTaskScreen } from "/task.js";
import { renderStats } from "/stats.js";

const view = document.getElementById("view");

const routes = [
  { pattern: /^\/?$|^\/today$/, name: "today", render: renderToday },
  { pattern: /^\/tasks$/, name: "tasks", render: renderTasks },
  { pattern: /^\/task\/([a-z0-9-]+)$/, name: "tasks", render: (m) => renderTaskScreen(view, m[1]) },
  { pattern: /^\/stats$/, name: "stats", render: () => renderStats(view) },
  { pattern: /^\/journal$/, name: "journal", render: renderJournal },
];

// Every screen registers its teardown here so that leaving a task stops its
// timer, its heartbeat and its event stream.
let disposeCurrent = null;

async function route() {
  const path = location.hash.replace(/^#/, "") || "/today";

  if (disposeCurrent) {
    disposeCurrent();
    disposeCurrent = null;
  }
  setStatus("");

  for (const r of routes) {
    const m = r.pattern.exec(path);
    if (!m) continue;
    highlightNav(r.name);
    clear(view);
    try {
      disposeCurrent = (await r.render(m)) || null;
    } catch (err) {
      mount(view, errorBox(err));
    }
    return;
  }

  highlightNav("");
  mount(clear(view), el("p", { class: "empty" }, "Такой страницы нет."));
}

function highlightNav(name) {
  for (const link of document.querySelectorAll("[data-nav]")) {
    link.classList.toggle("active", link.dataset.nav === name);
  }
}

window.addEventListener("hashchange", route);
window.addEventListener("DOMContentLoaded", route);
if (document.readyState !== "loading") route();

// ---------------------------------------------------------------- today

async function renderToday() {
  const data = await api("/api/today");

  mount(view,
    el("h1", {}, "Сегодня"),
    el("p", { class: "sub" }, fmtDay(data.day)),
    el("div", { class: "stats-row" },
      stat(data.streak, "дней подряд"),
      stat(data.solved_today, "решено сегодня"),
      stat(data.minutes_today ? `${data.minutes_today}м` : "0м", "за клавиатурой"),
      stat(`${data.solved_tasks}/${data.total_tasks}`, "задач освоено"),
    ),
  );

  const reviews = data.reviews || [];
  mount(view, el("h2", {}, "Повторить"));
  if (reviews.length === 0) {
    mount(view, el("p", { class: "empty" }, "Повторов на сегодня нет."));
  } else {
    mount(view, el("div", { class: "cards" }, reviews.map((t) => taskCard(t))));
  }

  mount(view, el("h2", {}, "Новая задача"));
  if (!data.new) {
    mount(view, el("p", { class: "empty" },
      "Все задачи уже в ротации. Добавь новую в ./tasks или просто повторяй."));
  } else {
    mount(view, el("div", { class: "cards" }, taskCard(data.new, true)));
  }
}

function stat(value, label) {
  return el("div", { class: "stat" }, el("b", {}, String(value)), el("span", {}, label));
}

function taskCard(t, isNew = false) {
  const pills = [];
  if (isNew) pills.push(el("span", { class: "pill new" }, "новая"));
  if (t.due_on && !isNew) pills.push(el("span", { class: "pill overdue" }, `повтор ${fmtDay(t.due_on)}`));
  if (t.changed) pills.push(el("span", { class: "pill changed" }, "задача изменилась"));
  if (t.mastered) pills.push(el("span", { class: "pill mastered" }, "освоена"));
  if (t.missing) pills.push(el("span", { class: "pill" }, "нет на диске"));

  const meta = [
    el("span", {}, t.topic),
    el("span", {}, `сложность ${t.difficulty}`),
    el("span", {}, `~${t.estimate_minutes} мин`),
  ];
  if (t.passes > 0) {
    meta.push(el("span", {}, `решена ${plural(t.passes, "раз", "раза", "раз")}`));
    meta.push(el("span", {}, `лучшее ${fmtShort(t.best_active_ms)}`));
  }
  if (!t.race) meta.push(el("span", {}, "без -race"));

  return el("a", { class: "card", href: `#/task/${t.id}` },
    el("div", { class: "card-head" }, el("span", { class: "card-title" }, t.title), pills),
    el("div", { class: "card-meta" }, meta),
  );
}

// ---------------------------------------------------------------- tasks

async function renderTasks() {
  const data = await api("/api/tasks");
  const tasks = data.tasks || [];

  mount(view, el("h1", {}, "Все задачи"),
    el("p", { class: "sub" }, plural(tasks.length, "задача", "задачи", "задач")));

  for (const e of data.errors || []) {
    mount(view, el("div", { class: "error-box" }, `${e.dir}: ${e.error}`));
  }

  const byTopic = new Map();
  for (const t of tasks) {
    if (!byTopic.has(t.topic)) byTopic.set(t.topic, []);
    byTopic.get(t.topic).push(t);
  }

  for (const [topic, list] of byTopic) {
    mount(view,
      el("h2", {}, topic),
      el("div", { class: "cards" }, list.map((t) => taskCard(t))),
    );
  }

  mount(view, el("div", { class: "actions" },
    el("button", {
      onclick: async (e) => {
        e.target.disabled = true;
        try {
          await api("/api/tasks/reload", { method: "POST" });
          route();
        } finally {
          e.target.disabled = false;
        }
      },
    }, "Перечитать ./tasks"),
  ));
}

// ---------------------------------------------------------------- journal

async function renderJournal() {
  const data = await api("/api/notes?limit=200");
  const notes = data.notes || [];

  mount(view,
    el("h1", {}, "Журнал расхождений"),
    el("p", { class: "sub" }, "Что я сделал криво. Это и есть персональная программа тренировок."),
  );

  if (notes.length === 0) {
    mount(view, el("p", { class: "empty" },
      "Пока пусто. Заметка появляется здесь после того, как ты запишешь её на экране разбора."));
    return;
  }

  const topics = [...new Set(notes.map((n) => n.topic))].sort();
  let active = "";

  const list = el("div", { class: "cards" });
  const filters = el("div", { class: "actions" });

  const draw = () => {
    mount(clear(list),
      notes
        .filter((n) => !active || n.topic === active)
        .map((n) =>
          el("div", { class: "card" },
            el("div", { class: "card-head" },
              el("a", { class: "card-title", href: `#/task/${n.task_id}` }, n.task_title),
              el("span", { class: "pill" }, n.topic),
            ),
            el("div", { class: "card-meta" }, el("span", {}, fmtDay(n.day))),
            el("p", { class: "note-body" }, n.body),
            el("div", { class: "actions" },
              el("button", {
                class: "danger",
                onclick: async () => {
                  await api(`/api/notes/${n.id}`, { method: "DELETE" });
                  route();
                },
              }, "Удалить"),
            ),
          ),
        ),
    );
  };

  const buttons = ["", ...topics].map((topic) =>
    el("button", {
      class: topic === active ? "primary" : "",
      onclick: () => {
        active = topic;
        for (const b of buttons) b.className = b.dataset.topic === active ? "primary" : "";
        draw();
      },
      dataset: { topic },
    }, topic || "все темы"),
  );
  mount(filters, buttons);

  mount(view, filters, list);
  draw();
}
