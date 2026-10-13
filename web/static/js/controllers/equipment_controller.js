import { Controller } from "@hotwired/stimulus"

// Shows on a form only the equipment offered with the rooms ticked, and
// switches off the fields of the rest so they aren't sent.
//
//   <form data-controller="equipment" data-action="change->equipment#sync">
//     <input type="checkbox" name="room" value="3">
//     <li data-equipment-target="row" data-equipment-rooms="3 5"> …
//
// Without JavaScript every row shows and the server checks the rooms.
export default class extends Controller {
  static targets = ["row"]

  connect() {
    this.sync()
  }

  sync(event) {
    if (event && event.target.name !== "room") return
    const picked = new Set(
      [...this.formElement.querySelectorAll('input[name="room"]:checked')].map((el) => el.value),
    )
    this.rowTargets.forEach((row) => {
      const offered = row.dataset.equipmentRooms.split(" ").some((id) => picked.has(id))
      row.hidden = !offered
      row.querySelectorAll("input").forEach((el) => (el.disabled = !offered))
    })
    const section = this.element
    section.hidden = !this.rowTargets.some((row) => !row.hidden)
  }

  get formElement() {
    return this.element.closest("form") || document
  }
}
