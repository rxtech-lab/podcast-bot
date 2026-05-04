// Package imagegen wraps the Vercel AI Gateway image-generation endpoint
// (OpenAI-compatible shape) so both the asset-generator command and the
// runtime puzzle scene generator can share one HTTP path.
package imagegen

// GatewayURL is the OpenAI-compatible image-generations endpoint exposed
// by the Vercel AI Gateway. Used for gpt-image-*, Imagen, and the other
// dedicated image models (the original gen-assets path).
const GatewayURL = "https://ai-gateway.vercel.sh/v1/images/generations"

// GatewayChatURL is the OpenAI-compatible chat-completions endpoint.
// Gemini's flash-image models are exposed via chat completions with
// modalities=["image"]; the resulting image rides back inside
// choices[0].message.images as a data: URL. Client.Generate auto-routes
// google/gemini-*-image* models through this path.
const GatewayChatURL = "https://ai-gateway.vercel.sh/v1/chat/completions"

// PuzzleSceneModel is the model slug used to generate puzzle scene
// backgrounds at runtime. Gemini's flash-image preview is fast and cheap
// (a few cents for the 4 scenes in a single puzzle), which matters because
// each puzzle topic regenerates from scratch.
const PuzzleSceneModel = "google/gemini-3.1-flash-image-preview"
