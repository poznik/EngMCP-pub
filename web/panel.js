for (const snippet of document.querySelectorAll(".client-snippet")) {
  const code = snippet.querySelector("pre");
  const status = snippet.querySelector('[role="status"]');
  let resetTimer;

  snippet.querySelector("button").addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(code.textContent);
      clearTimeout(resetTimer);
      snippet.classList.add("copied");
      status.textContent = "Скопировано";
      resetTimer = setTimeout(() => {
        status.textContent = "";
        snippet.classList.remove("copied");
      }, 2500);
    } catch {
      clearTimeout(resetTimer);
      snippet.classList.remove("copied");
      const range = document.createRange();
      range.selectNodeContents(code);
      const selection = window.getSelection();
      selection.removeAllRanges();
      selection.addRange(range);
      status.textContent = "Фрагмент выделен. Скопируй его через меню или Ctrl+C.";
    }
  });
}
