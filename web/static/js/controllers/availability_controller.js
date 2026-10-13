import { Controller } from "@hotwired/stimulus"

// Shows beside each room on the request form whether it's free at the
// times picked, from a same-origin JSON endpoint that only gives busy
// times (never titles).
//
//   <form data-controller="availability" data-availability-url-value="/request/availability"
//         data-action="change->availability#check">
//     <input name="starts_on" data-availability-target="field"> …
//     <span data-availability-target="room" data-room-id="3"></span>
//
// Without JavaScript the form still works; the scheduler sees clashes.
export default class extends Controller {
  static targets = ["field", "room"]
  static values = { url: String }

  connect() {
    this.check()
  }

  disconnect() {
    clearTimeout(this.timer)
    this.abort?.abort()
  }

  check(event) {
    if (event && !this.fieldTargets.includes(event.target)) return
    clearTimeout(this.timer)
    this.timer = setTimeout(() => this.load(), 250)
  }

  async load() {
    const params = new URLSearchParams()
    this.fieldTargets.forEach((field) => params.set(field.name, field.value))
    this.abort?.abort()
    this.abort = new AbortController()
    let data
    try {
      const response = await fetch(`${this.urlValue}?${params}`, {
        headers: { Accept: "application/json" },
        signal: this.abort.signal,
      })
      if (!response.ok) return this.show({})
      data = await response.json()
    } catch (error) {
      if (error.name !== "AbortError") this.show({})
      return
    }
    this.show(data.ready ? data.rooms : {})
  }

  show(rooms) {
    this.roomTargets.forEach((el) => {
      const status = rooms[el.dataset.roomId]
      el.classList.toggle("avail--free", !!status?.free)
      el.classList.toggle("avail--busy", !!status && !status.free)
      if (!status) el.textContent = ""
      else if (status.free) el.textContent = "Free"
      else el.textContent = `Busy ${status.busy.join(", ")}`
    })
  }
}
