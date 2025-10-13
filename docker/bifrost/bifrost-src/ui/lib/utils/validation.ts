export interface ValidationRule {
	isValid: boolean;
	message: string;
}

export interface ValidationConfig {
	rules: ValidationRule[];
	showAlways?: boolean; // If true, shows tooltip even when field is untouched
}

export interface FieldValidation {
	isValid: boolean;
	message: string;
	showTooltip: boolean;
}

export const validateField = (value: any, config: ValidationConfig, touched: boolean): FieldValidation => {
	const invalidRule = config.rules.find((rule) => !rule.isValid);

	return {
		isValid: !invalidRule,
		message: invalidRule?.message || "",
		showTooltip: config.showAlways || (touched && !!invalidRule),
	};
};

export interface ValidationResult {
	isValid: boolean;
	errors: string[];
}

export const validateForm = (rules: ValidationRule[]): ValidationResult => {
	const invalidRules = rules.filter((rule) => !rule.isValid);
	return {
		isValid: invalidRules.length === 0,
		errors: invalidRules.map((rule) => rule.message),
	};
};

export class Validator {
	private rules: ValidationRule[];

	constructor(rules: ValidationRule[]) {
		this.rules = rules.filter((rule) => rule !== undefined);
	}

	isValid(): boolean {
		return !this.rules.some((rule) => !rule.isValid);
	}

	getErrors(): string[] {
		return this.rules.filter((rule) => !rule.isValid).map((rule) => rule.message);
	}

	getFirstError(): string | undefined {
		const firstInvalidRule = this.rules.find((rule) => !rule.isValid);
		return firstInvalidRule?.message;
	}

	// Built-in validators
	static required(value: any, message = "This field is required"): ValidationRule {
		return {
			isValid: value !== undefined && value !== null && value !== "" && value !== 0,
			message,
		};
	}

	static minValue(value: number, min: number, message = `Must be at least ${min}`): ValidationRule {
		return {
			isValid: !isNaN(value) && value >= min,
			message,
		};
	}

	static maxValue(value: number, max: number, message = `Must be at most ${max}`): ValidationRule {
		return {
			isValid: !isNaN(value) && value <= max,
			message,
		};
	}

	static pattern(value: string, regex: RegExp, message: string): ValidationRule {
		return {
			isValid: regex.test(value || ""),
			message,
		};
	}

	static email(value: string, message = "Must be a valid email"): ValidationRule {
		return this.pattern(value, /^[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}$/i, message);
	}

	static url(value: string, message = "Must be a valid URL"): ValidationRule {
		return this.pattern(value, /^https?:\/\/.+/, message);
	}

	static minLength(value: string, min: number, message = `Must be at least ${min} characters`): ValidationRule {
		return {
			isValid: (value || "").length >= min,
			message,
		};
	}

	static maxLength(value: string, max: number, message = `Must be at most ${max} characters`): ValidationRule {
		return {
			isValid: (value || "").length <= max,
			message,
		};
	}

	static arrayMinLength<T>(array: T[], min: number, message = `Must have at least ${min} items`): ValidationRule {
		return {
			isValid: array?.length >= min,
			message,
		};
	}

	static arrayMaxLength<T>(array: T[], max: number, message = `Must have at most ${max} items`): ValidationRule {
		return {
			isValid: array?.length <= max,
			message,
		};
	}

	static arrayUnique<T>(array: T[], message = "Must have unique items"): ValidationRule {
		return {
			isValid: array?.length === new Set(array).size,
			message,
		};
	}

	static arraysEqual<T>(array1: T[], array2: T[], message = "Must be equal"): ValidationRule {
		return {
			isValid: array1?.length === array2?.length && array1?.every((value, index) => value === array2[index]),
			message,
		};
	}

	static custom(isValid: boolean, message: string): ValidationRule {
		return {
			isValid,
			message,
		};
	}

	// Combine multiple validation rules
	static all(rules: ValidationRule[]): ValidationRule {
		const invalidRule = rules.find((rule) => !rule.isValid);
		return invalidRule || { isValid: true, message: "" };
	}
}

// Utility functions for validation and redaction detection

/**
 * Checks if a value is redacted based on the backend redaction patterns
 * @param value - The value to check
 * @returns true if the value is redacted
 */
export function isRedacted(value: string): boolean {
	if (!value) {
		return false;
	}

	// Check if it's an environment variable reference
	if (value.startsWith("env.")) {
		return true;
	}

	// Check for exact redaction pattern: 4 chars + 24 asterisks + 4 chars (total 32)
	if (value.length === 32) {
		const middle = value.substring(4, 28);
		if (middle === "*".repeat(24)) {
			return true;
		}
	}

	// Check for short key redaction (all asterisks, length <= 8)
	if (value.length <= 8 && /^\*+$/.test(value)) {
		return true;
	}

	return false;
}

/**
 * Checks if a JSON string is valid
 * @param value - The JSON string to validate
 * @returns true if valid JSON
 */
export function isValidJSON(value: string): boolean {
	try {
		JSON.parse(value);
		return true;
	} catch {
		return false;
	}
}

/**
 * Validates Vertex auth credentials
 * @param value - The auth credentials value
 * @returns true if valid (redacted, env var, or valid service account JSON)
 */
export function isValidVertexAuthCredentials(value: string): boolean {
	if (!value || !value.trim()) {
		return false;
	}

	// If redacted, consider it valid (backend has the real value)
	if (isRedacted(value)) {
		return true;
	}

	// If environment variable, validate format
	if (value.startsWith("env.")) {
		return value.length > 4;
	}

	// Try to parse as service account JSON
	try {
		const parsed = JSON.parse(value);
		return typeof parsed === "object" && parsed !== null && parsed.type === "service_account" && parsed.project_id && parsed.private_key;
	} catch {
		return false;
	}
}

/**
 * Validates deployments configuration
 * @param value - The deployments value (object or string)
 * @returns true if valid (redacted, or valid JSON object)
 */
export function isValidDeployments(value: Record<string, string> | string | undefined): boolean {
	if (!value) {
		return false;
	}

	// If it's already an object, check if it has entries
	if (typeof value === "object") {
		return Object.keys(value).length > 0;
	}

	// If it's a string, check for redaction or valid JSON
	if (typeof value === "string") {
		// If redacted, consider it valid (backend has the real value)
		if (isRedacted(value)) {
			return true;
		}

		// Try to parse as JSON
		try {
			const parsed = JSON.parse(value);
			return typeof parsed === "object" && parsed !== null && Object.keys(parsed).length > 0;
		} catch {
			return false;
		}
	}

	return false;
}

/**
 * Validates if a string is a valid origin URL
 * @param origin - The origin URL to validate
 * @returns true if valid origin (protocol + hostname + optional port)
 */
export function isValidOrigin(origin: string): boolean {
	if (!origin || !origin.trim()) {
		return false;
	}

	try {
		const url = new URL(origin);

		// Must have protocol and hostname
		if (!url.protocol || !url.hostname) {
			return false;
		}

		// Must be http or https
		if (!["http:", "https:"].includes(url.protocol)) {
			return false;
		}

		// Must not have path, query, or fragment (origin should be just protocol + hostname + port)
		if (url.pathname !== "/" || url.search || url.hash) {
			return false;
		}

		return true;
	} catch {
		return false;
	}
}

/**
 * Validates an array of origin URLs
 * @param origins - Array of origin URLs to validate
 * @returns Object with validation result and invalid origins
 */
export function validateOrigins(origins: string[]): { isValid: boolean; invalidOrigins: string[] } {
	const invalidOrigins = origins.filter((origin) => !isValidOrigin(origin));

	return {
		isValid: invalidOrigins.length === 0,
		invalidOrigins,
	};
}

/**
 * Validates if a string is a valid Redis address
 * Supports formats:
 * - host:port (IPv4)
 * - [host]:port (IPv6)
 * - redis://host:port
 * - rediss://host:port
 * @param addr - The Redis address to validate
 * @returns true if valid Redis address
 */
export function isValidRedisAddress(addr: string): boolean {
	if (!addr) {
		return false;
	}

	// Trim input once before processing
	const trimmedAddr = addr.trim();
	if (!trimmedAddr) {
		return false;
	}

	try {
		// Handle URL schemes (redis:// or rediss://)
		if (trimmedAddr.startsWith("redis://") || trimmedAddr.startsWith("rediss://")) {
			try {
				const url = new URL(trimmedAddr);
				const host = url.hostname;
				const port = url.port || "6379"; // Default Redis port

				// Check if host is IPv6 (contains colons or is bracketed)
				const isIPv6Host = host.includes(":") || host.startsWith("[");
				const hostToValidate = isIPv6Host ? host.replace(/^\[|\]$/g, "") : host;

				const isValidHostResult = isIPv6Host ? isValidIPv6(hostToValidate) : isValidHost(hostToValidate);
				return isValidHostResult && isValidPort(port);
			} catch {
				return false;
			}
		}

		// Handle IPv6 addresses in brackets [host]:port
		const ipv6Match = trimmedAddr.match(/^\[([^\]]+)\]:(\d+)$/);
		if (ipv6Match) {
			const [, host, port] = ipv6Match;
			return isValidIPv6(host) && isValidPort(port);
		}

		// Handle standard host:port format
		const colonIndex = trimmedAddr.lastIndexOf(":");
		if (colonIndex === -1) {
			return false;
		}

		const host = trimmedAddr.substring(0, colonIndex);
		const port = trimmedAddr.substring(colonIndex + 1);

		// Validate both host and port
		return isValidHost(host) && isValidPort(port);
	} catch {
		return false;
	}
}

/**
 * Validates if a string is a valid host (hostname or IP address)
 * @param host - The host to validate
 * @returns true if valid host
 */
function isValidHost(host: string): boolean {
	if (!host || !host.trim()) {
		return false;
	}

	const trimmedHost = host.trim();

	// Check if this looks like an IPv6 address (contains colons or is bracketed)
	if (trimmedHost.includes(":") || trimmedHost.startsWith("[")) {
		// Strip brackets if present and validate as IPv6
		const ipv6Host = trimmedHost.replace(/^\[|\]$/g, "");
		return isValidIPv6(ipv6Host);
	}

	// Check for valid hostname/IPv4 patterns
	// Allow alphanumeric characters, dots, hyphens, and underscores
	const hostPattern = /^[a-zA-Z0-9._-]+$/;
	return hostPattern.test(trimmedHost) && trimmedHost.length <= 253;
}

/**
 * Validates if a string is a valid port number (strict digit-only validation)
 * @param port - The port to validate
 * @returns true if valid port
 */
function isValidPort(port: string): boolean {
	if (!port) {
		return false;
	}

	const trimmedPort = port.trim();

	// Port must consist only of digits (no trailing characters like "6379abc")
	if (!/^\d+$/.test(trimmedPort)) {
		return false;
	}

	// Convert to number and check range
	const portNum = Number(trimmedPort);
	return portNum >= 1 && portNum <= 65535;
}

/**
 * Validates if a string is a valid IPv6 address
 * @param host - The IPv6 address to validate (without brackets)
 * @returns true if valid IPv6 address
 */
function isValidIPv6(host: string): boolean {
	if (!host || !host.trim()) {
		return false;
	}

	const trimmedHost = host.trim();

	// Basic IPv6 pattern validation
	// IPv6 addresses contain colons and hexadecimal characters
	const ipv6Pattern =
		/^([0-9a-fA-F]{0,4}:){1,7}[0-9a-fA-F]{0,4}$|^::$|^::1$|^([0-9a-fA-F]{0,4}:){0,6}::([0-9a-fA-F]{0,4}:){0,6}[0-9a-fA-F]{0,4}$/;

	// Check basic pattern
	if (!ipv6Pattern.test(trimmedHost)) {
		// Also allow IPv6 with embedded IPv4 (e.g., ::ffff:192.168.1.1)
		const ipv6WithIpv4Pattern = /^([0-9a-fA-F]{0,4}:){1,6}(\d{1,3}\.){3}\d{1,3}$|^::([0-9a-fA-F]{0,4}:){0,5}(\d{1,3}\.){3}\d{1,3}$/;
		if (!ipv6WithIpv4Pattern.test(trimmedHost)) {
			return false;
		}
	}

	// Additional validation: check for valid hex groups and proper structure
	const parts = trimmedHost.split(":");

	// IPv6 should not have more than 8 groups (unless it's compressed with ::)
	if (parts.length > 8) {
		return false;
	}

	// Check for valid hexadecimal groups
	for (const part of parts) {
		if (part !== "" && !/^[0-9a-fA-F]{1,4}$/.test(part)) {
			// Allow IPv4 dotted notation in the last part
			if (!/^(\d{1,3}\.){3}\d{1,3}$/.test(part)) {
				return false;
			}
		}
	}

	return true;
}
