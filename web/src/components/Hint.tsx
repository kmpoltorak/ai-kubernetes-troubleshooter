import { useEffect, useId, useRef, useState, type ReactNode } from "react";

// A "?" that opens an explanation in a bubble beside it. The native popover
// puts it in the top layer (never clipped, never shifting the layout) and
// closes it on Escape or a click elsewhere; it works with touch and keyboard,
// unlike a hover tooltip. The button is named by its own text, not
// aria-label, so it never becomes a field's label.
export function Hint({ topic, children }: { topic: string; children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const id = useId();
  const button = useRef<HTMLButtonElement>(null);
  const bubble = useRef<HTMLSpanElement>(null);

  useEffect(() => {
    const el = bubble.current;
    if (!el) return;
    // Below the "?" when it fits, otherwise above; always inside the viewport.
    const place = () => {
      const b = button.current!.getBoundingClientRect();
      const width = Math.min(288, window.innerWidth - 16);
      el.style.width = `${width}px`;
      el.style.left = `${Math.min(Math.max(8, b.left + b.width / 2 - width / 2), window.innerWidth - width - 8)}px`;
      const below = b.bottom + 6, height = el.offsetHeight;
      el.style.top = `${below + height > window.innerHeight - 8 && b.top - 6 - height > 8 ? b.top - 6 - height : below}px`;
    };
    const hide = () => el.hidePopover();
    const toggle = (e: Event) => {
      const isOpen = (e as ToggleEvent).newState === "open";
      setOpen(isOpen);
      if (isOpen) {
        place();
        window.addEventListener("scroll", hide, { capture: true, once: true });
        window.addEventListener("resize", hide, { once: true });
      }
    };
    el.addEventListener("toggle", toggle);
    return () => { el.removeEventListener("toggle", toggle); window.removeEventListener("scroll", hide, { capture: true }); window.removeEventListener("resize", hide); };
  }, []);

  return (
    <>
      <button ref={button} type="button" className="hint" popoverTarget={id} aria-expanded={open} aria-controls={id}>
        <span aria-hidden>?</span>
        <span className="sr-only">What is {topic}?</span>
      </button>
      <span ref={bubble} id={id} popover="auto" className="hint-bubble">{children}</span>
    </>
  );
}

// A form field whose label row carries a hint; the label still names the control.
export function Field({ label, hint, children }: { label: ReactNode; hint?: ReactNode; children: (id: string) => ReactNode }) {
  const id = useId();
  const topic = typeof label === "string" ? label : "this field";
  return (
    <div className="label">
      <span className="flex flex-wrap items-center gap-1.5">
        <label htmlFor={id}>{label}</label>
        {hint && <Hint topic={topic}>{hint}</Hint>}
      </span>
      {children(id)}
    </div>
  );
}
