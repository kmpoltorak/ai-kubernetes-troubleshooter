import { useId, useState, type ReactNode } from "react";

// A "?" that expands an explanation in place: works with touch and keyboard, unlike a hover tooltip.
// The button is named by its own text, not aria-label, so it never becomes a field's label.
export function Hint({ topic, children }: { topic: string; children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const id = useId();
  return (
    <>
      <button type="button" className="hint" aria-expanded={open} aria-controls={id} onClick={() => setOpen(!open)}>
        <span aria-hidden>?</span>
        <span className="sr-only">What is {topic}?</span>
      </button>
      {open && <span id={id} className="hint-text">{children}</span>}
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
