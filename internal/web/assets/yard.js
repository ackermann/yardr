// yard.js is the view's own script: every page loads it from this server,
// and nothing in it names another host. What it does now:
//
// The talk page's textarea sends on ctrl+Enter, cmd+Enter on a Mac, as its
// Send button does; a plain Enter still breaks the line. The form stands
// outside the page's live part, so one listener on the document holds
// across htmx swaps. requestSubmit fires the submit event, which htmx
// takes as it takes the button's.
//
// The talk page names that key beside its Send button: ctrl+Enter,
// ⌘+Enter on a Mac. The template carries the element empty and hidden,
// so where this script does not run the page names no key, which is
// true. Each send puts a fresh form in place of the talk, so the hint
// is shown again after every swap.
document.addEventListener("keydown", function (event) {
  if (event.key !== "Enter" || !(event.ctrlKey || event.metaKey)) return;
  var target = event.target;
  if (!(target instanceof HTMLTextAreaElement) || target.id !== "text") return;
  if (!target.form) return;
  event.preventDefault();
  target.form.requestSubmit();
});

function showSendKey() {
  var hint = document.getElementById("send-key");
  if (!hint || !hint.hidden) return;
  var platform = "";
  if (navigator.userAgentData && typeof navigator.userAgentData.platform === "string") {
    platform = navigator.userAgentData.platform;
  } else if (typeof navigator.platform === "string") {
    platform = navigator.platform;
  }
  hint.textContent = platform === "macOS" || platform.indexOf("Mac") === 0 ? "⌘+Enter" : "ctrl+Enter";
  hint.hidden = false;
}
showSendKey();
document.addEventListener("htmx:afterSwap", showSendKey);
