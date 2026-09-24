// bulk.js (fix/admin-ops): row checkboxes + bulk bar for admin lists
// (Товары, Заказы). Markup contract:
//   <form id="X" data-bulk hidden method="post" ...>  — the bulk bar itself;
//     hidden until at least one row is checked.
//   <input type="checkbox" class="js-bulk-item" form="X" name="id" value="…">
//     — one per row, anywhere on the page (the form="" attribute submits it
//     with the bar even though it sits in the table).
//   <input type="checkbox" class="js-bulk-all" data-bulk-for="X"> — select all.
//   .js-bulk-count inside the bar — the checked count.
//   <button type="button" data-bulk-action="value" data-bulk-confirm="… {n} …"
//     [data-bulk-title="…"] [data-bulk-requires="field name"] [data-bulk-danger]>
//     — sets the bar's hidden input[name=action] (when value is non-empty),
//     then submits through the layout's shared confirm dialog
//     (form[data-confirm], layout.gohtml).
(function () {
  function init(form) {
    // getAttribute, not form.id: the row checkboxes are named "id" and
    // belong to this form, so form.id resolves to them, not the form's id.
    var id = form.getAttribute('id');
    var itemSel = 'input.js-bulk-item[form="' + id + '"]';
    var all = document.querySelector('input.js-bulk-all[data-bulk-for="' + id + '"]');

    function items() { return Array.prototype.slice.call(document.querySelectorAll(itemSel)); }
    function checkedCount() { return items().filter(function (c) { return c.checked; }).length; }
    function sync() {
      var list = items();
      var n = list.filter(function (c) { return c.checked; }).length;
      form.querySelectorAll('.js-bulk-count').forEach(function (el) { el.textContent = n; });
      form.hidden = n === 0;
      if (all) {
        all.checked = n > 0 && n === list.length;
        all.indeterminate = n > 0 && n < list.length;
      }
    }

    document.addEventListener('change', function (e) {
      if (e.target.matches && e.target.matches(itemSel)) sync();
    });
    if (all) {
      all.addEventListener('change', function () {
        items().forEach(function (c) { c.checked = all.checked; });
        sync();
      });
    }

    form.querySelectorAll('[data-bulk-confirm]').forEach(function (btn) {
      btn.addEventListener('click', function () {
        var n = checkedCount();
        if (n === 0) return;
        var req = btn.getAttribute('data-bulk-requires');
        if (req) {
          var field = form.elements[req];
          if (field && !field.value) { field.focus(); return; }
        }
        var action = btn.getAttribute('data-bulk-action');
        if (action && form.elements.action) form.elements.action.value = action;
        var text = btn.getAttribute('data-bulk-confirm').replace('{n}', n);
        var extra = req && form.elements[req] && form.elements[req].selectedOptions
          ? form.elements[req].selectedOptions[0].textContent.trim() : '';
        form.setAttribute('data-confirm', text.replace('{value}', extra));
        form.setAttribute('data-confirm-title', btn.getAttribute('data-bulk-title') || '');
        form.setAttribute('data-confirm-label', btn.textContent.trim());
        if (btn.hasAttribute('data-bulk-danger')) { form.setAttribute('data-confirm-danger', ''); } else { form.removeAttribute('data-confirm-danger'); }
        form.requestSubmit();
      });
    });
    sync();
  }
  function start() { document.querySelectorAll('form[data-bulk]').forEach(init); }
  if (document.readyState === 'loading') { document.addEventListener('DOMContentLoaded', start); } else { start(); }
})();
