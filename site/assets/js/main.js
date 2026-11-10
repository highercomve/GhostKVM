// Copy buttons on the install snippets, and the hero animation paused for
// people who prefer reduced motion. The page works fully without this file.

document.querySelectorAll(".code .copy").forEach((btn) => {
  btn.addEventListener("click", async () => {
    const code = btn.closest(".code").querySelector("pre").innerText.trim();
    try {
      await navigator.clipboard.writeText(code);
      btn.textContent = "Copied";
      btn.classList.add("done");
    } catch {
      btn.textContent = "Select and copy";
    }
    setTimeout(() => {
      btn.textContent = "Copy";
      btn.classList.remove("done");
    }, 1800);
  });
});

const stage = document.getElementById("stage");
const reduce = window.matchMedia("(prefers-reduced-motion: reduce)");
function applyMotion() {
  if (!stage || !stage.pauseAnimations) return;
  if (reduce.matches) {
    stage.setCurrentTime(3.5); // ghost resting on the Chromebook side
    stage.pauseAnimations();
  } else {
    stage.unpauseAnimations();
  }
}
applyMotion();
reduce.addEventListener?.("change", applyMotion);
