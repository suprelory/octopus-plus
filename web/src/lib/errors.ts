// API errors are plain objects; native Error instances expose the same message
// field. Keep useful text intact and let each caller supply its fallback.
export function getErrorMessage(error: unknown, fallback: string): string {
    if (error && typeof error === 'object' && 'message' in error) {
        const message = error.message;
        if (typeof message === 'string' && message.trim()) {
            return message;
        }
    }
    return fallback;
}
