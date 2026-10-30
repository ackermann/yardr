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
//
// The talk page's letters are grouped by the reader's day and clock, which
// the browser sets over the whole pane (groupLetters, below).
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

// The talk page's letters are grouped by the day and the clock of the
// reader, which only the browser knows: the server groups them by its own,
// which is the page as it reads where this script does not run, and its
// first paint. A letter reads under the head above it, its time in the
// gutter, when it is from the same sender, within five minutes of that
// letter and on the same day; a letter that opens a day carries the day's
// separator. That is talk.go's grouped, read again here in the browser's
// zone over the whole pane: the newest page and the older pages htmx has put
// above it, which no one page of the server sees across. The classes are the
// template's (head, day, differs); the times and the day's words are
// written in the browser's zone.
var WEEKDAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];
var MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

function sameDay(a, b) {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

// The day's separator: Today, Yesterday, or the weekday with the day and the
// month (Saturday 17 Oct), as talk.go's dayWords words it.
function dayWords(day, now) {
  if (sameDay(day, now)) return "Today";
  if (sameDay(day, new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1))) return "Yesterday";
  return WEEKDAYS[day.getDay()] + " " + day.getDate() + " " + MONTHS[day.getMonth()];
}

function pad(n) {
  return n < 10 ? "0" + n : String(n);
}

function groupLetters() {
  // The pane holds the live block before the older ones in the document and
  // shows them after: the seqs are the order of the mail.
  var letters = Array.prototype.slice.call(document.querySelectorAll("#letters .letter"));
  letters.sort(function (a, b) { return a.dataset.seq - b.dataset.seq; });
  var now = new Date();
  var before = null;
  letters.forEach(function (letter) {
    var time = letter.querySelector("time");
    var at = new Date(time.getAttribute("datetime"));
    // A time that cannot be read stands on its own, as the server's page
    // has it.
    if (isNaN(at.getTime())) {
      before = null;
      return;
    }
    var who = letter.querySelector(".who").textContent;
    var head = true, day = true, differs = false;
    if (before) {
      day = !sameDay(before.at, at);
      var gap = Math.abs(Math.floor(at.getTime() / 1000) - Math.floor(before.at.getTime() / 1000));
      head = day || letter.dataset.from !== before.from || gap > 300;
      differs = !head && who !== before.who;
    }
    letter.classList.toggle("head", head);
    letter.classList.toggle("day", day);
    letter.classList.toggle("differs", differs);
    time.textContent = pad(at.getHours()) + ":" + pad(at.getMinutes());
    letter.querySelector(".day-break").textContent = day ? dayWords(at, now) : "";
    before = { at: at, from: letter.dataset.from, who: who };
  });
}
groupLetters();
document.addEventListener("htmx:afterSwap", groupLetters);
