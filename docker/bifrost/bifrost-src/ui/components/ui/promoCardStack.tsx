"use client";

import { cn } from "@/lib/utils";
import { X } from "lucide-react";
import React, { useEffect, useState } from "react";
import { Card, CardContent, CardHeader } from "./card";

interface PromoCardItem {
	id: string;
	title: string | React.ReactElement;
	description: string | React.ReactElement;
	dismissible?: boolean;
}

interface PromoCardStackProps {
	cards: PromoCardItem[];
	className?: string;
}

export function PromoCardStack({ cards, className = "" }: PromoCardStackProps) {
	const [items, setItems] = useState(() => {
		return [...cards].sort((a, b) => {
			const aDismissible = a.dismissible !== false;
			const bDismissible = b.dismissible !== false;
			return bDismissible === aDismissible ? 0 : aDismissible ? -1 : 1;
		});
	});
	const [removingId, setRemovingId] = useState<string | null>(null);
	const [isAnimating, setIsAnimating] = useState(false);

	useEffect(() => {
		const sortedCards = [...cards].sort((a, b) => {
			const aDismissible = a.dismissible !== false;
			const bDismissible = b.dismissible !== false;
			return bDismissible === aDismissible ? 0 : aDismissible ? -1 : 1;
		});
		setItems(sortedCards);
	}, [cards]);

	const handleDismiss = (cardId: string) => {
		if (isAnimating) return;
		setIsAnimating(true);
		setRemovingId(cardId);

		setTimeout(() => {
			setItems((prev) => prev.filter((it) => it.id !== cardId));
			setRemovingId(null);
			setIsAnimating(false);
		}, 400);
	};

	if (!cards || cards.length === 0) {
		return null;
	}

	const MAX_VISIBLE_CARDS = 10;
	const visibleCards = items.slice(0, MAX_VISIBLE_CARDS);

	return (
		<div className={`relative ${className}`} style={{ marginBottom: "60px", height: "130px" }}>
			{visibleCards.map((card, index) => {
				const isTopCard = index === 0;
				const isRemoving = removingId === card.id;
				const scale = 1 - index * 0.05;
				const yOffset = index * 10;
				const opacity = 1 - index * 0.2;

				return (
					<div
						key={card.id}
						className="absolute right-0 left-0 transition-all duration-400 ease-out"
						style={{
							top: isRemoving ? 0 : `${yOffset}px`,
							transform: isRemoving ? "translateX(-120%) rotate(-8deg)" : `scale(${scale})`,
							opacity: isRemoving ? 0 : opacity,
							zIndex: visibleCards.length - index,
							transformOrigin: "center center",
							pointerEvents: isTopCard && !isAnimating ? "auto" : "none",
							height: "180px",
						}}
					>
						<Card
							className={cn(
								"flex h-full w-full flex-col gap-0 rounded-lg px-2.5 py-2",
								visibleCards.length < 2 ? "shadow-none" : "shadow-md",
							)}
						>
							<CardHeader className="text-muted-foreground flex-shrink-0 p-1 text-sm font-medium">
								<div className="flex items-start justify-between">
									<div className="min-w-0 flex-1">{typeof card.title === "string" ? card.title : card.title}</div>
									{card.dismissible !== false && isTopCard && (
										<button
											aria-label="Dismiss"
											type="button"
											onClick={() => handleDismiss(card.id)}
											disabled={isAnimating}
											className="hover:text-foreground text-muted-foreground -m-1 flex-shrink-0 rounded p-1 disabled:opacity-50"
										>
											<X className="h-3.5 w-3.5" />
										</button>
									)}
								</div>
							</CardHeader>
							<CardContent className="text-muted-foreground mt-0 flex-1 overflow-y-auto px-1 pt-0 pb-1 text-xs">
								{typeof card.description === "string" ? card.description : card.description}
							</CardContent>
						</Card>
					</div>
				);
			})}
		</div>
	);
}
