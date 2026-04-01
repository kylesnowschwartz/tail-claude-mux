// Minimum width that prevents wrapping in the footer keybindings bar.
// Sessions footer: "⇥ cycle  ⏎ go  → agents  d hide  x kill" = ~37 cols
// with 1 col paddingLeft on the footer box.
export const MIN_SIDEBAR_WIDTH = 38;

export function clampSidebarWidth(width: number): number {
  return Math.max(MIN_SIDEBAR_WIDTH, width);
}
