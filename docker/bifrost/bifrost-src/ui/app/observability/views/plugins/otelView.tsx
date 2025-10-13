"use client";

import { Input } from "@/components/ui/input";
import { getErrorMessage, useAppSelector, useUpdatePluginMutation } from "@/lib/store";
import { OtelConfigSchema, OtelFormSchema } from "@/lib/types/schemas";
import { useMemo } from "react";
import { toast } from "sonner";
import { OtelFormFragment } from "../../fragments/otelFormFragment";

export default function OtelView() {
	const selectedPlugin = useAppSelector((state) => state.plugin.selectedPlugin);
	const currentConfig = useMemo(
		() => ({ ...((selectedPlugin?.config as OtelConfigSchema) ?? {}), enabled: selectedPlugin?.enabled }),
		[selectedPlugin],
	);
	const [updatePlugin, { isLoading: isUpdatingPlugin }] = useUpdatePluginMutation();
	const baseUrl = `${window.location.protocol}//${window.location.host}`;

	const handleOtelConfigSave = (config: OtelFormSchema): Promise<void> => {
		return new Promise((resolve, reject) => {
			updatePlugin({
				name: "otel",
				data: {
					enabled: config.enabled,
					config: config.otel_config,
				},
			})
				.unwrap()
				.then(() => {
					resolve();
					toast.success("OTEL configuration updated successfully");
				})
				.catch((err) => {
					toast.error("Failed to update OTEL configuration", {
						description: getErrorMessage(err),
					});
					reject(err);
				});
		});
	};

	return (
		<div className="flex w-full flex-col gap-4">
			<div className="border-secondary flex w-full flex-col gap-2 rounded-sm border p-4">
				<div className="text-muted-foreground mb-2 text-xs font-medium">Metrics (scraping endpoint)</div>
				<Input className="bg-accent mb-2 font-mono" value={`${baseUrl}/metrics`} readOnly showCopyButton />
			</div>
			<div className="border-secondary flex w-full flex-col gap-3 rounded-sm border px-4 py-2">
				<div className="text-muted-foreground mb-2 text-xs font-medium">Traces Configuration</div>
				<OtelFormFragment onSave={handleOtelConfigSave} currentConfig={currentConfig} />
			</div>
		</div>
	);
}
