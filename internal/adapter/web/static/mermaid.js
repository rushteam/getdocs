// 按需加载 mermaid 并渲染页面中的图表；主题随页面切换。
const script = document.querySelector("script[data-src]");
const nodes = [...document.querySelectorAll("pre.mermaid")];
const sources = nodes.map((n) => n.textContent);

const { default: mermaid } = await import(script.dataset.src);

async function draw() {
  const dark = document.documentElement.getAttribute("data-theme") === "dark";
  mermaid.initialize({ startOnLoad: false, theme: dark ? "dark" : "default", securityLevel: "strict" });
  nodes.forEach((n, i) => {
    n.removeAttribute("data-processed");
    n.textContent = sources[i];
  });
  try {
    await mermaid.run({ nodes });
  } catch (e) {
    console.error("mermaid render failed", e);
  }
}

await draw();
new MutationObserver(draw).observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
