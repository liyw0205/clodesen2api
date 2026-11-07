(() => {
  "use strict";
  window.createRoutingTools = ({ element, onChange }) => {
    const labels = { fixed: "固定", round_robin: "轮询", random: "随机" };
    const disclosure = element("details", "update-source-disclosure");
    disclosure.id = "routing-disclosure";
    const trigger = element("summary", "update-source-trigger");
    trigger.id = "routing-strategy";
    trigger.setAttribute("aria-label", "账号选择策略");
    const current = element("strong", "", labels.fixed);
    current.id = "routing-current";
    const arrow = element("span", "update-source-trigger-icon", "⌄");
    arrow.setAttribute("aria-hidden", "true");
    trigger.append(element("span", "update-source-trigger-label", "路由"), current, arrow);
    const panel = element("fieldset", "floating-panel routing-panel");
    panel.id = "routing-panel";
    panel.append(element("legend", "", "账号选择策略"));
    const options = element("div", "routing-options");
    options.setAttribute("role", "radiogroup");
    options.setAttribute("aria-label", "账号选择策略");
    const radios = Object.entries(labels).map(([value, label]) => {
      const row = element("label", "update-source-option");
      const radio = element("input");
      radio.type = "radio"; radio.name = "routing-strategy"; radio.value = value;
      radio.checked = value === "fixed";
      radio.addEventListener("change", () => { floating.close(true); void onChange(value); });
      row.append(radio, element("span", "update-source-option-name", label));
      options.append(row);
      return radio;
    });
    panel.append(options);
    disclosure.append(trigger, panel);
    document.querySelector(".strategy-control").replaceChildren(disclosure);
    const floating = window.createFloatingPanel({ disclosure, panel, width: 240 });
    trigger.addEventListener("click", event => {
      if (trigger.getAttribute("aria-disabled") === "true") event.preventDefault();
    });
    function set(strategy) {
      current.textContent = labels[strategy] || labels.fixed;
      radios.forEach(radio => { radio.checked = radio.value === (labels[strategy] ? strategy : "fixed"); });
    }
    function busy(value) {
      trigger.setAttribute("aria-disabled", String(value));
      radios.forEach(radio => { radio.disabled = value; });
    }
    return { set, busy, clear() { floating.close(); busy(false); set("fixed"); } };
  };
})();
