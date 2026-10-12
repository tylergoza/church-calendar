import { Controller } from "@hotwired/stimulus"

// The "Repeats" fields of the event form. Shows only the parts that apply
// to the chosen frequency and keeps a plain-English summary, worded like the
// server's Rule.Describe. Without JavaScript every part shows and the server
// ignores the ones that don't apply.
//
//   <fieldset data-controller="repeat" data-action="change->repeat#update input->repeat#update">
//     <input type="radio" name="repeat_freq" value="weekly">
//     <div data-repeat-target="part" data-repeat-for="weekly monthly">…</div>
//     <p data-repeat-target="summary"></p>
const DAYS = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"]
const MONTHS = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"]

function ordinal(n) {
  let suffix = "th"
  if (n % 100 < 11 || n % 100 > 13) suffix = { 1: "st", 2: "nd", 3: "rd" }[n % 10] || "th"
  return `${n}${suffix}`
}

function joinNames(names) {
  if (names.length <= 1) return names.join("")
  return `${names.slice(0, -1).join(", ")} and ${names[names.length - 1]}`
}

export default class extends Controller {
  static targets = ["part", "summary"]

  // The weekday, day-of-month and nth-weekday defaults follow the form's
  // start date until someone changes one of them by hand.
  connect() {
    this.touched = false
    this.start = this.element.form?.querySelector('[name="starts_on"]')
    this.onStart = () => this.followStart()
    this.onTouch = (e) => {
      if (/^repeat_(weekday|month_day|month_week|month_weekday)$/.test(e.target.name)) this.touched = true
    }
    this.start?.addEventListener("change", this.onStart)
    this.element.addEventListener("change", this.onTouch)
    this.update()
  }

  disconnect() {
    this.start?.removeEventListener("change", this.onStart)
    this.element.removeEventListener("change", this.onTouch)
  }

  followStart() {
    const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(this.start.value)
    if (this.touched || !m) return
    const d = new Date(Date.UTC(+m[1], +m[2] - 1, +m[3]))
    const weekday = d.getUTCDay()
    const day = d.getUTCDate()
    const week = Math.ceil(day / 7)
    this.element.querySelectorAll('[name="repeat_weekday"]').forEach((el) => {
      el.checked = parseInt(el.value, 10) === weekday
    })
    const set = (name, v) => {
      const el = this.element.querySelector(`[name="${name}"]`)
      if (el) el.value = String(v)
    }
    set("repeat_month_day", day)
    set("repeat_month_week", week > 4 ? -1 : week)
    set("repeat_month_weekday", weekday)
    this.update()
  }

  value(name) {
    return this.element.querySelector(`[name="${name}"]:checked`)?.value
  }

  field(name) {
    return this.element.querySelector(`[name="${name}"]`)?.value
  }

  update() {
    const freq = this.value("repeat_freq") || "none"
    this.partTargets.forEach((part) => {
      part.hidden = !part.dataset.repeatFor.split(/\s+/).includes(freq)
    })
    if (this.hasSummaryTarget) this.summaryTarget.textContent = this.describe(freq)
  }

  describe(freq) {
    if (freq === "none") return ""
    const every = parseInt(this.field("repeat_every"), 10)
    if (!(every >= 1)) return ""
    let s
    if (freq === "daily") {
      s = every === 1 ? "Every day" : `Every ${every} days`
    } else if (freq === "weekly") {
      const days = [...this.element.querySelectorAll('[name="repeat_weekday"]:checked')]
        .map((el) => parseInt(el.value, 10)).sort((a, b) => a - b).map((d) => DAYS[d])
      const on = days.length ? joinNames(days) : "the start date's weekday"
      s = every === 1 ? `Every week on ${on}` : `Every ${every} weeks on ${on}`
    } else {
      let when
      if (this.value("repeat_month_mode") === "weekday") {
        const week = parseInt(this.field("repeat_month_week"), 10)
        when = `the ${week === -1 ? "last" : ordinal(week)} ${DAYS[parseInt(this.field("repeat_month_weekday"), 10)]}`
      } else {
        const day = parseInt(this.field("repeat_month_day"), 10)
        if (!(day >= 1 && day <= 31)) return ""
        when = `the ${ordinal(day)}`
      }
      s = every === 1 ? `Every month on ${when}` : `Every ${every} months on ${when}`
    }
    const end = this.value("repeat_end")
    if (end === "on") {
      const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(this.field("repeat_until") || "")
      if (m) s += `, until ${parseInt(m[3], 10)} ${MONTHS[parseInt(m[2], 10) - 1]} ${m[1]}`
    } else if (end === "after") {
      const n = parseInt(this.field("repeat_count"), 10)
      if (n === 1) s += ", once"
      else if (n > 1) s += `, ${n} times`
    }
    return s
  }
}
