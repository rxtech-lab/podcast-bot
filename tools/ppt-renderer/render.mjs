import fs from "node:fs/promises";
import path from "node:path";
import pptxgen from "pptxgenjs";

const [, , inputPath, outputPath] = process.argv;

if (!inputPath || !outputPath) {
  console.error("usage: node render.mjs <deck.json> <output.pptx>");
  process.exit(2);
}

const spec = JSON.parse(await fs.readFile(inputPath, "utf8"));
validateDeck(spec);

await fs.mkdir(path.dirname(outputPath), { recursive: true });

const pptx = new pptxgen();
pptx.layout = "LAYOUT_WIDE";
pptx.author = "PanelStations";
pptx.company = "RxLab";
pptx.subject = "Podcast summary slide deck";
pptx.title = clean(spec.title, "Podcast Summary");
pptx.lang = "en-US";
pptx.theme = {
  headFontFace: "Aptos Display",
  bodyFontFace: "Aptos",
  lang: "en-US",
};

const W = 13.333;
const H = 7.5;
const colors = {
  ink: "111827",
  navy: "0F172A",
  slate: "334155",
  muted: "64748B",
  line: "CBD5E1",
  paleLine: "E2E8F0",
  page: "F8FAFC",
  card: "FFFFFF",
  cyan: "06B6D4",
  blue: "2563EB",
  green: "10B981",
  amber: "F59E0B",
  violet: "7C3AED",
};
const accents = [colors.cyan, colors.green, colors.amber, colors.blue, colors.violet];

pptx.defineSlideMaster({
  title: "SUMMARY",
  background: { color: colors.page },
  objects: [
    { line: { x: 0.45, y: 7.05, w: 12.45, h: 0, line: { color: colors.paleLine, width: 1 } } },
    { text: { text: "PanelStations", options: { x: 0.55, y: 7.12, w: 2.8, h: 0.18, fontFace: "Aptos", fontSize: 7.5, color: colors.muted, margin: 0 } } },
  ],
  slideNumber: { x: 12.15, y: 7.1, color: colors.muted, fontFace: "Aptos", fontSize: 7.5 },
});

addTitleSlide(pptx, spec);
for (const [idx, slideSpec] of spec.slides.entries()) {
  addContentSlide(pptx, slideSpec, idx);
}

await pptx.writeFile({ fileName: outputPath });

function addTitleSlide(pptx, spec) {
  const slide = pptx.addSlide("SUMMARY");
  slide.background = { color: colors.page };
  slide.addShape(pptx.ShapeType.rect, { x: 0, y: 0, w: W, h: H, fill: { color: colors.page }, line: { color: colors.page } });
  slide.addShape(pptx.ShapeType.rect, { x: 0, y: 0, w: 8.75, h: H, fill: { color: colors.navy }, line: { color: colors.navy } });
  slide.addShape(pptx.ShapeType.rect, { x: 8.75, y: 0, w: 0.12, h: H, fill: { color: colors.cyan }, line: { color: colors.cyan } });
  slide.addShape(pptx.ShapeType.rect, { x: 8.87, y: 0, w: 0.08, h: H, fill: { color: colors.amber }, line: { color: colors.amber } });

  slide.addShape(pptx.ShapeType.roundRect, { x: 0.72, y: 0.68, w: 2.3, h: 0.34, rectRadius: 0.05, fill: { color: "1E293B" }, line: { color: "1E293B" } });
  slide.addText("Podcast summary", {
    x: 0.87,
    y: 0.76,
    w: 2.0,
    h: 0.15,
    margin: 0,
    fontFace: "Aptos",
    fontSize: 10,
    bold: true,
    color: colors.cyan,
    charSpace: 0,
    breakLine: false,
  });

  const title = clean(spec.title, "Podcast Summary");
  slide.addText(title, {
    x: 0.72,
    y: 1.42,
    w: 7.35,
    h: 1.55,
    margin: 0,
    fontFace: "Aptos Display",
    fontSize: fitTitle(title),
    bold: true,
    color: "FFFFFF",
    breakLine: false,
    fit: "shrink",
    valign: "mid",
    charSpace: 0,
  });

  const subtitle = clean(spec.subtitle, "");
  if (subtitle) {
    slide.addText(subtitle, {
      x: 0.75,
      y: 3.12,
      w: 6.9,
      h: 0.68,
      margin: 0,
      fontFace: "Aptos",
      fontSize: 18,
      color: "CBD5E1",
      fit: "shrink",
      breakLine: false,
      charSpace: 0,
    });
  }

  slide.addShape(pptx.ShapeType.line, { x: 0.75, y: 4.25, w: 3.25, h: 0, line: { color: colors.cyan, width: 3 } });
  slide.addShape(pptx.ShapeType.line, { x: 4.1, y: 4.25, w: 0.9, h: 0, line: { color: colors.amber, width: 3 } });

  const outline = spec.slides.map((s) => clean(s.title, "")).filter(Boolean).slice(0, 5);
  slide.addShape(pptx.ShapeType.roundRect, { x: 9.45, y: 0.82, w: 3.05, h: 5.9, rectRadius: 0.08, fill: { color: colors.card }, line: { color: colors.paleLine, width: 1 } });
  slide.addText("Outline", {
    x: 9.72,
    y: 1.13,
    w: 2.5,
    h: 0.28,
    margin: 0,
    fontFace: "Aptos Display",
    fontSize: 15,
    bold: true,
    color: colors.ink,
    charSpace: 0,
  });
  outline.forEach((item, idx) => {
    const y = 1.72 + idx * 0.86;
    const accent = accents[idx % accents.length];
    slide.addShape(pptx.ShapeType.roundRect, { x: 9.72, y, w: 0.38, h: 0.38, rectRadius: 0.04, fill: { color: accent }, line: { color: accent } });
    slide.addText(String(idx + 1).padStart(2, "0"), {
      x: 9.79,
      y: y + 0.095,
      w: 0.24,
      h: 0.1,
      margin: 0,
      fontFace: "Aptos",
      fontSize: 6.8,
      bold: true,
      color: "FFFFFF",
      charSpace: 0,
    });
    slide.addText(item, {
      x: 10.22,
      y: y - 0.02,
      w: 1.92,
      h: 0.48,
      margin: 0,
      fontFace: "Aptos",
      fontSize: outlineFontSize(item),
      color: colors.slate,
      fit: "shrink",
      valign: "mid",
      charSpace: 0,
    });
  });
}

function addContentSlide(pptx, slideSpec, idx) {
  const slide = pptx.addSlide("SUMMARY");
  const accent = accents[idx % accents.length];
  const bullets = cleanList(slideSpec.bullets, 5);
  const opinions = cleanOpinions(slideSpec.speakerOpinions, 3);
  const visual = cleanVisual(slideSpec.visual);
  const hasVisual = Boolean(visual.title || visual.data.length);
  const hasOpinions = opinions.length > 0;
  const summary = clean(slideSpec.summary, bullets[0] ?? "");
  const takeaway = clean(slideSpec.takeaway, clean(slideSpec.notes, ""));
  const kicker = clean(slideSpec.kicker, `Section ${idx + 1}`);
  const title = clean(slideSpec.title, "Slide");

  slide.background = { color: colors.page };
  slide.addShape(pptx.ShapeType.rect, { x: 0, y: 0, w: W, h: H, fill: { color: colors.page }, line: { color: colors.page } });
  slide.addShape(pptx.ShapeType.rect, { x: 0, y: 0, w: 0.16, h: H, fill: { color: accent }, line: { color: accent } });
  slide.addText(`SECTION ${String(idx + 1).padStart(2, "0")} / ${kicker}`, {
    x: 0.62,
    y: 0.43,
    w: 5.8,
    h: 0.2,
    margin: 0,
    fontFace: "Aptos",
    fontSize: 9.5,
    bold: true,
    color: accent,
    fit: "shrink",
    charSpace: 0,
  });
  slide.addText(title, {
    x: 0.62,
    y: 0.73,
    w: 11.95,
    h: 0.54,
    margin: 0,
    fontFace: "Aptos Display",
    fontSize: fitSlideTitle(title),
    bold: true,
    color: colors.ink,
    fit: "shrink",
    breakLine: false,
    charSpace: 0,
  });

  slide.addShape(pptx.ShapeType.roundRect, { x: 0.62, y: 1.54, w: 3.42, h: 4.95, rectRadius: 0.08, fill: { color: colors.navy }, line: { color: colors.navy } });
  slide.addText(String(idx + 1).padStart(2, "0"), {
    x: 0.92,
    y: 1.87,
    w: 1.0,
    h: 0.45,
    margin: 0,
    fontFace: "Aptos Display",
    fontSize: 28,
    bold: true,
    color: accent,
    charSpace: 0,
  });
  slide.addShape(pptx.ShapeType.line, { x: 0.92, y: 2.54, w: 1.15, h: 0, line: { color: accent, width: 2 } });
  slide.addText(summary, {
    x: 0.92,
    y: 2.82,
    w: 2.58,
    h: 1.35,
    margin: 0,
    fontFace: "Aptos",
    fontSize: sideSummaryFontSize(summary),
    color: "E2E8F0",
    fit: "shrink",
    valign: "top",
    charSpace: 0,
    breakLine: false,
  });
  if (takeaway) {
    slide.addShape(pptx.ShapeType.roundRect, { x: 0.88, y: 4.72, w: 2.62, h: 1.08, rectRadius: 0.06, fill: { color: "1E293B" }, line: { color: "1E293B" } });
    slide.addShape(pptx.ShapeType.rect, { x: 0.88, y: 4.72, w: 0.08, h: 1.08, fill: { color: accent }, line: { color: accent } });
    slide.addText(takeaway, {
      x: 1.08,
      y: 4.93,
      w: 2.18,
      h: 0.55,
      margin: 0,
      fontFace: "Aptos",
      fontSize: takeawayFontSize(takeaway),
      bold: true,
      color: "FFFFFF",
      fit: "shrink",
      valign: "mid",
      charSpace: 0,
    });
  }

  const mainX = 4.45;
  const mainW = 8.12;
  let contentY = 1.54;
  if (hasVisual) {
    addVisualPanel(slide, visual, accent, mainX, contentY, mainW, 0.96);
    contentY += 1.12;
  }

  const cardGap = 0.13;
  const cardBottom = 6.02;
  const cardAreaH = Math.max(1.15, cardBottom - contentY);
  const cardX = mainX;
  const cardW = hasOpinions ? 4.72 : mainW;
  const shownBullets = bullets.slice(0, hasOpinions ? 4 : 5);
  const cardH = Math.min(hasOpinions ? 0.78 : 0.96, (cardAreaH - cardGap * Math.max(0, shownBullets.length - 1)) / Math.max(1, shownBullets.length));
  shownBullets.forEach((text, bulletIdx) => {
    const y = contentY + bulletIdx * (cardH + cardGap);
    slide.addShape(pptx.ShapeType.roundRect, { x: cardX, y, w: cardW, h: cardH, rectRadius: 0.06, fill: { color: colors.card }, line: { color: colors.paleLine, width: 1 } });
    slide.addShape(pptx.ShapeType.rect, { x: cardX, y, w: 0.09, h: cardH, fill: { color: accent }, line: { color: accent } });
    slide.addShape(pptx.ShapeType.ellipse, { x: cardX + 0.31, y: y + 0.2, w: 0.32, h: 0.32, fill: { color: accent }, line: { color: accent } });
    slide.addText(String(bulletIdx + 1), {
      x: cardX + 0.31,
      y: y + 0.295,
      w: 0.32,
      h: 0.08,
      margin: 0,
      fontFace: "Aptos",
      fontSize: 6.8,
      bold: true,
      color: "FFFFFF",
      align: "center",
      charSpace: 0,
    });
    slide.addText(text, {
      x: cardX + 0.82,
      y: y + 0.13,
      w: cardW - 1.12,
      h: cardH - 0.18,
      margin: 0,
      fontFace: "Aptos",
      fontSize: bulletCardFontSize(text, shownBullets.length, hasOpinions),
      color: colors.slate,
      fit: "shrink",
      valign: "mid",
      breakLine: false,
      charSpace: 0,
    });
  });

  if (hasOpinions) {
    addSpeakerOpinionCards(slide, opinions, accent, 9.45, contentY, 3.12, cardAreaH);
  }

  if (takeaway) {
    slide.addShape(pptx.ShapeType.roundRect, { x: 4.45, y: 6.18, w: 8.12, h: 0.32, rectRadius: 0.05, fill: { color: "EEF2FF" }, line: { color: "EEF2FF" } });
    slide.addText(takeaway, {
      x: 4.65,
      y: 6.265,
      w: 7.68,
      h: 0.1,
      margin: 0,
      fontFace: "Aptos",
      fontSize: 8.5,
      bold: true,
      color: colors.slate,
      fit: "shrink",
      breakLine: false,
      charSpace: 0,
    });
  }
}

function addVisualPanel(slide, visual, accent, x, y, w, h) {
  slide.addShape(pptx.ShapeType.roundRect, { x, y, w, h, rectRadius: 0.06, fill: { color: colors.card }, line: { color: colors.paleLine, width: 1 } });
  slide.addShape(pptx.ShapeType.rect, { x, y, w: 0.09, h, fill: { color: accent }, line: { color: accent } });
  slide.addShape(pptx.ShapeType.roundRect, { x: x + 0.28, y: y + 0.24, w: 1.05, h: 0.24, rectRadius: 0.04, fill: { color: "ECFEFF" }, line: { color: "ECFEFF" } });
  slide.addText(visual.kind.toUpperCase(), {
    x: x + 0.4,
    y: y + 0.315,
    w: 0.82,
    h: 0.07,
    margin: 0,
    fontFace: "Aptos",
    fontSize: 6.6,
    bold: true,
    color: accent,
    fit: "shrink",
    charSpace: 0,
  });
  slide.addText(clean(visual.title, "Visual frame"), {
    x: x + 1.55,
    y: y + 0.2,
    w: w - 4.2,
    h: 0.22,
    margin: 0,
    fontFace: "Aptos Display",
    fontSize: 12.5,
    bold: true,
    color: colors.ink,
    fit: "shrink",
    charSpace: 0,
  });

  visual.data.slice(0, 3).forEach((item, itemIdx) => {
    slide.addShape(pptx.ShapeType.roundRect, { x: x + 1.55 + itemIdx * 1.35, y: y + 0.57, w: 1.2, h: 0.22, rectRadius: 0.04, fill: { color: "F1F5F9" }, line: { color: "F1F5F9" } });
    slide.addText(item, {
      x: x + 1.65 + itemIdx * 1.35,
      y: y + 0.64,
      w: 1.0,
      h: 0.07,
      margin: 0,
      fontFace: "Aptos",
      fontSize: 6.5,
      color: colors.slate,
      fit: "shrink",
      charSpace: 0,
    });
  });

  drawVisualGlyph(slide, visual, accent, x + w - 2.68, y + 0.2, 2.32, 0.56);
}

function drawVisualGlyph(slide, visual, accent, x, y, w, h) {
  const data = visual.data.length ? visual.data : ["A", "B", "C"];
  const kind = visual.kind.toLowerCase();
  if (kind === "metric") {
    slide.addText(clean(data[0], "Key"), {
      x,
      y: y + 0.04,
      w: 1.05,
      h: 0.18,
      margin: 0,
      fontFace: "Aptos Display",
      fontSize: 15,
      bold: true,
      color: accent,
      fit: "shrink",
      charSpace: 0,
    });
    slide.addShape(pptx.ShapeType.line, { x: x + 1.2, y: y + 0.28, w: 0.9, h: 0, line: { color: accent, width: 4 } });
    slide.addShape(pptx.ShapeType.ellipse, { x: x + 1.98, y: y + 0.16, w: 0.25, h: 0.25, fill: { color: accent }, line: { color: accent } });
    return;
  }
  if (kind === "timeline" || kind === "cycle") {
    slide.addShape(pptx.ShapeType.line, { x, y: y + h / 2, w, h: 0, line: { color: colors.line, width: 1.4 } });
    data.slice(0, 4).forEach((_, idx) => {
      const cx = x + (w - 0.26) * (idx / Math.max(1, Math.min(3, data.length - 1)));
      slide.addShape(pptx.ShapeType.ellipse, { x: cx, y: y + h / 2 - 0.13, w: 0.26, h: 0.26, fill: { color: idx === 0 ? accent : "FFFFFF" }, line: { color: accent, width: 1.2 } });
    });
    return;
  }
  if (kind === "stack") {
    data.slice(0, 4).forEach((_, idx) => {
      const barH = 0.12 + idx * 0.08;
      slide.addShape(pptx.ShapeType.rect, { x: x + idx * 0.48, y: y + h - barH, w: 0.34, h: barH, fill: { color: idx % 2 === 0 ? accent : colors.amber }, line: { color: idx % 2 === 0 ? accent : colors.amber } });
    });
    return;
  }
  if (kind === "spectrum") {
    slide.addShape(pptx.ShapeType.line, { x, y: y + h / 2, w, h: 0, line: { color: accent, width: 3 } });
    slide.addShape(pptx.ShapeType.ellipse, { x: x - 0.02, y: y + h / 2 - 0.12, w: 0.24, h: 0.24, fill: { color: "FFFFFF" }, line: { color: accent, width: 1.2 } });
    slide.addShape(pptx.ShapeType.ellipse, { x: x + w - 0.22, y: y + h / 2 - 0.12, w: 0.24, h: 0.24, fill: { color: accent }, line: { color: accent, width: 1.2 } });
    return;
  }
  data.slice(0, 2).forEach((_, idx) => {
    const bx = x + idx * 1.12;
    slide.addShape(pptx.ShapeType.roundRect, { x: bx, y: y + 0.08, w: 0.88, h: 0.38, rectRadius: 0.04, fill: { color: idx === 0 ? "ECFEFF" : "FEF3C7" }, line: { color: idx === 0 ? accent : colors.amber, width: 1 } });
  });
  slide.addShape(pptx.ShapeType.line, { x: x + 0.9, y: y + 0.28, w: 0.22, h: 0, line: { color: colors.line, width: 1.2 } });
}

function addSpeakerOpinionCards(slide, opinions, accent, x, y, w, h) {
  const gap = 0.12;
  const cardH = Math.min(1.04, (h - gap * Math.max(0, opinions.length - 1)) / Math.max(1, opinions.length));
  opinions.forEach((opinion, idx) => {
    const rowY = y + idx * (cardH + gap);
    slide.addShape(pptx.ShapeType.roundRect, { x, y: rowY, w, h: cardH, rectRadius: 0.06, fill: { color: "F8FAFC" }, line: { color: colors.paleLine, width: 1 } });
    slide.addShape(pptx.ShapeType.ellipse, { x: x + 0.22, y: rowY + 0.18, w: 0.34, h: 0.34, fill: { color: accent }, line: { color: accent } });
    slide.addText(initials(opinion.speaker), {
      x: x + 0.22,
      y: rowY + 0.285,
      w: 0.34,
      h: 0.08,
      margin: 0,
      fontFace: "Aptos",
      fontSize: 6.6,
      bold: true,
      color: "FFFFFF",
      align: "center",
      charSpace: 0,
    });
    slide.addText(opinion.speaker, {
      x: x + 0.68,
      y: rowY + 0.14,
      w: w - 0.88,
      h: 0.17,
      margin: 0,
      fontFace: "Aptos",
      fontSize: 8.5,
      bold: true,
      color: colors.ink,
      fit: "shrink",
      charSpace: 0,
    });
    slide.addText(opinion.opinion, {
      x: x + 0.22,
      y: rowY + 0.56,
      w: w - 0.45,
      h: cardH > 0.9 ? 0.27 : 0.2,
      margin: 0,
      fontFace: "Aptos",
      fontSize: opinionFontSize(opinion.opinion, opinions.length),
      color: colors.slate,
      fit: "shrink",
      breakLine: false,
      charSpace: 0,
    });
    if (opinion.evidence && cardH > 0.82) {
      slide.addText(opinion.evidence, {
        x: x + 0.22,
        y: rowY + 0.83,
        w: w - 0.45,
        h: 0.13,
        margin: 0,
        fontFace: "Aptos",
        fontSize: 6.8,
        italic: true,
        color: colors.muted,
        fit: "shrink",
        breakLine: false,
        charSpace: 0,
      });
    }
  });
}

function cleanList(values, max) {
  if (!Array.isArray(values)) return [];
  return values.map((value) => clean(value, "")).filter(Boolean).slice(0, max);
}

function cleanOpinions(values, max) {
  if (!Array.isArray(values)) return [];
  return values
    .map((value) => ({
      speaker: clean(value?.speaker, ""),
      opinion: clean(value?.opinion, ""),
      evidence: clean(value?.evidence, ""),
    }))
    .filter((value) => value.speaker && value.opinion)
    .slice(0, max);
}

function cleanVisual(value) {
  if (!value || typeof value !== "object") return { kind: "compare", title: "", data: [] };
  const kind = clean(value.kind, "compare").toLowerCase();
  return {
    kind: ["spectrum", "compare", "timeline", "stack", "cycle", "metric"].includes(kind) ? kind : "compare",
    title: clean(value.title, ""),
    data: cleanList(value.data, 4),
  };
}

function bulletCardFontSize(text, count, compact = false) {
  if (compact) {
    if (count >= 4 || text.length > 120) return 12.5;
    if (text.length > 80) return 13.5;
    return 14.5;
  }
  if (count >= 5 || text.length > 130) return 14;
  if (text.length > 90) return 15;
  return 16.5;
}

function opinionFontSize(text, count) {
  if (count >= 3 || text.length > 100) return 8;
  if (text.length > 70) return 8.8;
  return 9.5;
}

function initials(name) {
  return clean(name, "?")
    .split(/\s+/)
    .slice(0, 2)
    .map((part) => Array.from(part)[0] ?? "")
    .join("")
    .toUpperCase()
    .slice(0, 2);
}

function sideSummaryFontSize(text) {
  if (text.length > 150) return 13.5;
  if (text.length > 95) return 15;
  return 16.5;
}

function takeawayFontSize(text) {
  if (text.length > 120) return 10.5;
  if (text.length > 70) return 11.5;
  return 12.5;
}

function outlineFontSize(text) {
  if (text.length > 48) return 9.2;
  if (text.length > 32) return 10.2;
  return 11.2;
}

function fitSlideTitle(title) {
  if (title.length > 72) return 23;
  if (title.length > 46) return 25;
  return 28;
}

function fitTitle(title) {
  if (title.length > 90) return 32;
  if (title.length > 58) return 38;
  return 45;
}

function clean(value, fallback) {
  const text = String(value ?? "").replace(/\s+/g, " ").trim();
  return text || fallback;
}

function validateDeck(spec) {
  if (!spec || typeof spec !== "object") throw new Error("deck spec must be an object");
  if (!Array.isArray(spec.slides) || spec.slides.length === 0) throw new Error("deck spec missing slides");
  for (const [idx, slide] of spec.slides.entries()) {
    if (!slide || typeof slide !== "object") throw new Error(`slide ${idx + 1} must be an object`);
    if (!clean(slide.title, "")) throw new Error(`slide ${idx + 1} missing title`);
    if (!Array.isArray(slide.bullets) || slide.bullets.length === 0) throw new Error(`slide ${idx + 1} missing bullets`);
  }
}
