/* Progressive enhancement: every measurement remains readable without JavaScript. */
(() => {
  function enhance() {
    const ko = document.documentElement.lang === "ko";
    document.querySelectorAll(".ip-performance-table").forEach((section, index) => {
      if (section.dataset.enhanced) return;
      const table = section.querySelector("table");
      if (!table || !table.tBodies.length) return;
      section.dataset.enhanced = "true";
      const rows = Array.from(table.tBodies[0].rows);
      const controls = document.createElement("div");
      controls.className = "ip-performance-controls";
      const label = document.createElement("label");
      label.textContent = ko ? "모델 이름 검색" : "Search model names";
      const input = document.createElement("input");
      input.type = "search";
      input.id = `performance-search-${index}`;
      input.placeholder = "Claude, GPT, Qwen, Kimi…";
      label.htmlFor = input.id;
      const count = document.createElement("output");
      count.setAttribute("aria-live", "polite");
      input.addEventListener("input", () => {
        const query = input.value.trim().toLocaleLowerCase();
        let visible = 0;
        rows.forEach((row) => {
          row.hidden = !row.cells[0].textContent.toLocaleLowerCase().includes(query);
          if (!row.hidden) visible += 1;
        });
        count.textContent = ko ? `${visible} / ${rows.length}개 모델` : `${visible} / ${rows.length} models`;
      });
      controls.append(label, input, count);
      section.prepend(controls);
      input.dispatchEvent(new Event("input"));
      const scroll = section.querySelector(".md-typeset__scrollwrap") || table.parentElement;
      scroll.tabIndex = 0;
      scroll.setAttribute("role", "region");
      scroll.setAttribute("aria-label", ko ? "모델 측정 표. 가로로 스크롤할 수 있습니다." : "Model measurements. Scroll horizontally.");
    });
  }
  if (typeof document$ !== "undefined") document$.subscribe(enhance);
  else if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", enhance);
  else enhance();
})();
