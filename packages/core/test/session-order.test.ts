import { describe, test, expect, afterEach } from "bun:test";
import { existsSync, unlinkSync, mkdirSync, readFileSync } from "fs";
import { join } from "path";
import { tmpdir } from "os";
import { SessionOrder } from "../src/server/session-order";

describe("SessionOrder", () => {
  test("sync with initial sessions preserves natural order", () => {
    const order = new SessionOrder();
    order.sync(["a", "b", "c"]);
    expect(order.apply(["a", "b", "c"])).toEqual(["a", "b", "c"]);
  });

  test("reorder moves a session up (delta -1)", () => {
    const order = new SessionOrder();
    order.sync(["a", "b", "c"]);
    order.reorder("b", -1);
    expect(order.apply(["a", "b", "c"])).toEqual(["b", "a", "c"]);
  });

  test("reorder moves a session down (delta 1)", () => {
    const order = new SessionOrder();
    order.sync(["a", "b", "c"]);
    order.reorder("a", 1);
    expect(order.apply(["a", "b", "c"])).toEqual(["b", "a", "c"]);
  });

  test("reorder at top with delta -1 is a no-op", () => {
    const order = new SessionOrder();
    order.sync(["a", "b", "c"]);
    order.reorder("a", -1);
    expect(order.apply(["a", "b", "c"])).toEqual(["a", "b", "c"]);
  });

  test("reorder at bottom with delta 1 is a no-op", () => {
    const order = new SessionOrder();
    order.sync(["a", "b", "c"]);
    order.reorder("c", 1);
    expect(order.apply(["a", "b", "c"])).toEqual(["a", "b", "c"]);
  });

  test("sync adds new sessions at the end", () => {
    const order = new SessionOrder();
    order.sync(["a", "b"]);
    order.sync(["a", "b", "c"]);
    expect(order.apply(["a", "b", "c"])).toEqual(["a", "b", "c"]);
  });

  test("sync removes deleted sessions from order", () => {
    const order = new SessionOrder();
    order.sync(["a", "b", "c"]);
    order.reorder("c", -1); // c, b order becomes [a, c, b]
    order.sync(["a", "b"]); // c was deleted
    expect(order.apply(["a", "b"])).toEqual(["a", "b"]);
  });

  test("multiple reorders compose correctly", () => {
    const order = new SessionOrder();
    order.sync(["a", "b", "c", "d"]);
    order.reorder("d", -1); // [a, b, d, c]
    order.reorder("d", -1); // [a, d, b, c]
    expect(order.apply(["a", "b", "c", "d"])).toEqual(["a", "d", "b", "c"]);
  });

  test("hide removes a session from the visible panel order", () => {
    const order = new SessionOrder();
    order.sync(["a", "b", "c"]);
    order.hide("b");
    expect(order.apply(["a", "b", "c"])).toEqual(["a", "c"]);
  });

  test("show restores a hidden session", () => {
    const order = new SessionOrder();
    order.sync(["a", "b", "c"]);
    order.hide("b");
    order.show("b");
    expect(order.apply(["a", "b", "c"])).toEqual(["a", "b", "c"]);
  });

  test("showAll restores every hidden session", () => {
    const order = new SessionOrder();
    order.sync(["a", "b", "c"]);
    order.hide("a");
    order.hide("c");
    order.showAll();
    expect(order.apply(["a", "b", "c"])).toEqual(["a", "b", "c"]);
  });
});

describe("SessionOrder persistence", () => {
  const testDir = join(tmpdir(), "opensessions-test-" + process.pid);
  const persistPath = join(testDir, "session-order.json");

  afterEach(() => {
    try { unlinkSync(persistPath); } catch {}
  });

  test("reorder persists to disk", () => {
    mkdirSync(testDir, { recursive: true });
    const order = new SessionOrder(persistPath);
    order.sync(["a", "b", "c"]);
    order.reorder("c", -1);

    expect(existsSync(persistPath)).toBe(true);
    const saved = JSON.parse(readFileSync(persistPath, "utf-8"));
    expect(saved).toEqual(["a", "c", "b"]);
  });

  test("new instance loads persisted order", () => {
    mkdirSync(testDir, { recursive: true });
    const order1 = new SessionOrder(persistPath);
    order1.sync(["a", "b", "c"]);
    order1.reorder("c", -1); // [a, c, b]

    const order2 = new SessionOrder(persistPath);
    order2.sync(["a", "b", "c"]);
    expect(order2.apply(["a", "b", "c"])).toEqual(["a", "c", "b"]);
  });

  test("hidden sessions persist to disk", () => {
    mkdirSync(testDir, { recursive: true });
    const order = new SessionOrder(persistPath);
    order.sync(["a", "b", "c"]);
    order.hide("b");

    expect(existsSync(persistPath)).toBe(true);
    const saved = JSON.parse(readFileSync(persistPath, "utf-8"));
    expect(saved).toEqual({ order: ["a", "b", "c"], hidden: ["b"] });
  });

  test("new instance loads persisted hidden sessions", () => {
    mkdirSync(testDir, { recursive: true });
    Bun.write(
      persistPath,
      JSON.stringify({ order: ["a", "b", "c"], hidden: ["b"] }),
    );

    const order = new SessionOrder(persistPath);
    order.sync(["a", "b", "c"]);
    expect(order.apply(["a", "b", "c"])).toEqual(["a", "c"]);
  });

  test("showAll clears persisted hidden sessions", () => {
    mkdirSync(testDir, { recursive: true });
    const order = new SessionOrder(persistPath);
    order.sync(["a", "b", "c"]);
    order.hide("b");
    order.showAll();

    const saved = JSON.parse(readFileSync(persistPath, "utf-8"));
    expect(saved).toEqual(["a", "b", "c"]);
  });

  test("no persist path means no file written", () => {
    const order = new SessionOrder();
    order.sync(["a", "b"]);
    order.reorder("b", -1);
    // No crash, no file — just works in-memory
    expect(order.apply(["a", "b"])).toEqual(["b", "a"]);
  });

  test("corrupt file is ignored gracefully", () => {
    mkdirSync(testDir, { recursive: true });
    Bun.write(persistPath, "not valid json{{{");
    const order = new SessionOrder(persistPath);
    order.sync(["x", "y"]);
    expect(order.apply(["x", "y"])).toEqual(["x", "y"]);
  });
});
