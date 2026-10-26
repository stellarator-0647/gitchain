import type { Message } from "./types.ts";

/** Joins history pages (oldest first), keeping the first copy of an event that slid across a page boundary. */
export function mergePages(pages: Message[][]): Message[] {
  const seen = new Set<string>();
  const out: Message[] = [];
  for (const page of pages) {
    for (const m of page) {
      const id = m.eventId.toLowerCase();
      if (seen.has(id)) continue;
      seen.add(id);
      out.push(m);
    }
  }
  return out;
}
