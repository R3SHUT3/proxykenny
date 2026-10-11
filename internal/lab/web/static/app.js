// Glisser-déposer : envoie automatiquement le fichier déposé.
const drop = document.getElementById("drop");
if (drop) {
  const input = drop.querySelector("input");
  const label = drop.querySelector("span");
  ["dragenter", "dragover"].forEach(e => drop.addEventListener(e, ev => { ev.preventDefault(); drop.classList.add("over"); }));
  ["dragleave", "drop"].forEach(e => drop.addEventListener(e, () => drop.classList.remove("over")));
  drop.addEventListener("drop", ev => {
    ev.preventDefault();
    if (ev.dataTransfer.files.length) { input.files = ev.dataTransfer.files; drop.closest("form").submit(); }
  });
  input.addEventListener("change", () => { if (input.files.length) label.textContent = input.files[0].name; });
}

// Copie de l'URL signée.
document.querySelectorAll("[data-copy]").forEach(btn => btn.addEventListener("click", () => {
  const el = document.getElementById(btn.dataset.copy);
  el.select();
  navigator.clipboard?.writeText(el.value).then(() => { btn.textContent = "Copié ✓"; setTimeout(() => btn.textContent = "Copier", 1500); });
}));

// Banc d'essai : indique que le test tourne.
const bf = document.getElementById("benchform");
if (bf) bf.addEventListener("submit", () => {
  const b = document.getElementById("benchbtn");
  const d = bf.querySelector("[name=d]").value;
  b.disabled = true;
  let left = Number(d);
  b.textContent = `Test en cours… ${left} s`;
  setInterval(() => { left = Math.max(0, left - 1); b.textContent = left ? `Test en cours… ${left} s` : "Calcul des résultats…"; }, 1000);
});
