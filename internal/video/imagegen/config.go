// Package imagegen wraps the image-generation endpoints used by runtime media
// generation: Vercel AI Gateway for OpenAI-compatible image models and Google's
// native Interactions API for Gemini image models.
package imagegen

// GatewayURL is the OpenAI-compatible image-generations endpoint exposed
// by the Vercel AI Gateway. Used for gpt-image-*, Imagen, and the other
// dedicated image models (the original gen-assets path).
var GatewayURL = "https://ai-gateway.vercel.sh/v1/images/generations"

// GeminiInteractionsURL is Google's native Interactions API endpoint for
// Nano Banana image models.
var GeminiInteractionsURL = "https://generativelanguage.googleapis.com/v1beta/interactions"

// PuzzleSceneModel is the model slug used to generate puzzle scene
// backgrounds at runtime. Gemini's flash-image preview is fast and cheap
// (a few cents for the 4 scenes in a single puzzle), which matters because
// each puzzle topic regenerates from scratch.
const PuzzleSceneModel = "google/gemini-3.1-flash-image-preview"
