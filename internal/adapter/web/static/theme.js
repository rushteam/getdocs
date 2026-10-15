// 在首屏渲染前确定主题，避免闪烁。data-theme 总是 light 或 dark，代码高亮样式依赖它。
(function () {
  var saved = null;
  try { saved = localStorage.getItem("theme"); } catch (e) {}
  var dark = saved ? saved === "dark" : window.matchMedia("(prefers-color-scheme: dark)").matches;
  document.documentElement.setAttribute("data-theme", dark ? "dark" : "light");
})();
