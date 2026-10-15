// yard.js is the view's own script: every page loads it from this server,
// and nothing in it names another host. What it does now:
//
// The talk page's textarea sends on ctrl+Enter, cmd+Enter on a Mac, as its
// Send button does; a plain Enter still breaks the line. The form stands
// outside the page's live part, so one listener on the document holds
// across htmx swaps. requestSubmit fires the submit event, which htmx
// takes as it takes the button's.
document.addEventListener("keydown", function (event) {
  if (event.key !== "Enter" || !(event.ctrlKey || event.metaKey)) return;
  var target = event.target;
  if (!(target instanceof HTMLTextAreaElement) || target.id !== "text") return;
  if (!target.form) return;
  event.preventDefault();
  target.form.requestSubmit();
});
