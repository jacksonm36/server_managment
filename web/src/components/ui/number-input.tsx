import * as React from "react";
import { Input } from "@/components/ui/input";

// NumberInput is a number field you can clear and type into freely: it
// keeps what is typed and reports a number only when the text is one, so
// emptying the field does not snap it to 0. Leaving the field with no
// valid number puts the last value back.
export function NumberInput({
  value,
  onValue,
  onBlur,
  ...props
}: Omit<React.ComponentProps<"input">, "value" | "onChange" | "type"> & { value: number; onValue: (n: number) => void }) {
  const [text, setText] = React.useState(String(value));
  // Follow outside changes (a preset, a reset) unless they are what is typed.
  React.useEffect(() => {
    setText((t) => (t.trim() !== "" && Number(t) === value ? t : String(value)));
  }, [value]);
  return (
    <Input
      {...props}
      type="number"
      inputMode="numeric"
      value={text}
      onChange={(e) => {
        const t = e.target.value;
        setText(t);
        if (t.trim() !== "" && !Number.isNaN(Number(t))) onValue(Number(t));
      }}
      onBlur={(e) => {
        if (text.trim() === "" || Number.isNaN(Number(text))) setText(String(value));
        onBlur?.(e);
      }}
    />
  );
}
