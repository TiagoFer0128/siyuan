const update = (inputElement: HTMLInputElement, clearElement: Element, right: number) => {
    if (inputElement.value === "") {
        clearElement.classList.add("fn__none");
        if (right) {
            inputElement.style.paddingRight = "";
        }
    } else {
        clearElement.classList.remove("fn__none");
        if (right) {
            inputElement.style.setProperty("padding-right", `${right * 2 + clearElement.clientWidth}px`, "important");
        }
    }
};
export const addClearButton = (options: {
    inputElement: HTMLInputElement,
    clearCB?: () => void,
    right?: number,
    height?: number
    className?: string
}) => {
    options.inputElement.insertAdjacentHTML("afterend",
        `<svg class="${options.className || "b3-form__icon-clear"}" style="${options.right ? "right: " + options.right + "px;" : ""}${options.height ? "height:" + options.height + "px" : ""}">
<use xlink:href="#iconCloseRound"></use></svg>`);
    const clearElement = options.inputElement.nextElementSibling;
    clearElement.addEventListener("click", () => {
        options.inputElement.value = "";
        options.inputElement.focus();
        update(options.inputElement, clearElement, options.right);
        if (options.clearCB) {
            options.clearCB();
        }
    });
    options.inputElement.addEventListener("input", () => {
        update(options.inputElement, clearElement, options.right);
    });
    update(options.inputElement, clearElement, options.right);
};
