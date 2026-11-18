import { useId, useState } from "react";
import { HelpCircle, Send } from "lucide-react";
import type { QuestionState } from "@/conversation/state";
import { Button } from "@/ui/Button";

/** An ask_user question, answered by the requester right here in the chat. */
export function QuestionCard({
  question,
  disabled,
  onAnswer,
}: {
  question: QuestionState;
  disabled: boolean;
  onAnswer: (answers: string[][]) => void;
}) {
  const { request, answers } = question;
  const [picked, setPicked] = useState<string[][]>(() => request.questions.map(() => []));
  const [other, setOther] = useState<string[]>(() => request.questions.map(() => ""));
  const baseId = useId();

  if (answers) {
    return (
      <div className="question is-answered">
        <HelpCircle size={18} aria-hidden className="muted" />
        <div>
          {request.questions.map((q, i) => (
            <p key={i}>
              <span className="muted">{q.question}</span> <strong>{(answers[i] ?? []).join(", ") || "—"}</strong>
            </p>
          ))}
        </div>
      </div>
    );
  }

  const final = request.questions.map((_, i) => {
    const typed = other[i]?.trim();
    return typed ? [...(picked[i] ?? []), typed] : (picked[i] ?? []);
  });
  const complete = final.every((a) => a.length > 0);

  return (
    <form
      className="question"
      onSubmit={(e) => {
        e.preventDefault();
        if (complete) onAnswer(final);
      }}
    >
      <div className="question-head">
        <HelpCircle size={18} aria-hidden />
        <strong>{request.askedBy ? `${request.askedBy} has a question` : "A quick question"}</strong>
      </div>
      {request.questions.map((q, i) => (
        <fieldset key={i} className="question-field">
          <legend>{q.question}</legend>
          {q.choices.map((choice) => {
            const checked = picked[i]?.includes(choice) ?? false;
            return (
              <label key={choice} className={`choice ${checked ? "is-checked" : ""}`}>
                <input
                  type={q.multiple ? "checkbox" : "radio"}
                  name={`${baseId}-${i}`}
                  checked={checked}
                  disabled={disabled}
                  onChange={() =>
                    setPicked((prev) =>
                      prev.map((p, j) =>
                        j !== i ? p : q.multiple ? (checked ? p.filter((c) => c !== choice) : [...p, choice]) : [choice],
                      ),
                    )
                  }
                />
                <span>{choice}</span>
              </label>
            );
          })}
          <label className="sr-only" htmlFor={`${baseId}-${i}-other`}>
            {q.choices.length ? "Or type your own answer" : "Your answer"}
          </label>
          <input
            id={`${baseId}-${i}-other`}
            className="input"
            placeholder={q.choices.length ? "Or type your own answer" : "Type your answer"}
            value={other[i] ?? ""}
            disabled={disabled}
            onChange={(e) => setOther((prev) => prev.map((o, j) => (j === i ? e.target.value : o)))}
          />
        </fieldset>
      ))}
      <div>
        <Button type="submit" variant="primary" icon={<Send size={16} />} disabled={disabled || !complete}>
          Send answer
        </Button>
      </div>
    </form>
  );
}
