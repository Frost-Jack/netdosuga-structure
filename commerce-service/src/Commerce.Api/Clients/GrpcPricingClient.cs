using Commerce.Domain.Ports;
using Grpc.Net.Client;
using P = Netdosuga.Pricing.V1;

namespace Commerce.Api.Clients;

public sealed class GrpcPricingClient : IPricingClient
{
    private readonly GrpcChannel channel;
    private readonly P.Pricing.PricingClient client;

    public GrpcPricingClient(string address)
    {
        var normalized = address.StartsWith("http://", StringComparison.OrdinalIgnoreCase) ||
                         address.StartsWith("https://", StringComparison.OrdinalIgnoreCase)
            ? address
            : $"http://{address}";
        channel = GrpcChannel.ForAddress(normalized);
        client = new P.Pricing.PricingClient(channel);
    }

    public async Task<CurrentPrice> GetCurrentPriceAsync(string offerId, long basePriceCents, int available, int capacity, CancellationToken cancellationToken)
    {
        var response = await client.CurrentPriceAsync(new P.CurrentPriceRequest
        {
            OfferId = offerId,
            BasePriceCents = basePriceCents,
            Available = available,
            Capacity = capacity
        }, cancellationToken: cancellationToken);
        return new CurrentPrice(response.PriceCents);
    }

    public async Task<PricingQuote> QuoteAsync(string customerId, IReadOnlyList<QuoteLine> lines, string? promoCode, long bonusToSpend, CancellationToken cancellationToken)
    {
        var request = new P.QuoteRequest
        {
            CustomerId = customerId,
            PromoCode = promoCode ?? string.Empty,
            BonusToSpend = bonusToSpend
        };
        request.Lines.Add(lines.Select(ToProtoLine));
        var response = await client.QuoteAsync(request, cancellationToken: cancellationToken);
        return new PricingQuote(
            response.TotalCents,
            response.DiscountCents,
            response.Items.Select(x => new PricingQuoteItem(x.OfferId, x.UnitPriceCents, x.TotalCents, x.DiscountCents)).ToList());
    }

    public async Task CommitAsync(string orderId, string customerId, long totalCents, IReadOnlyList<QuoteLine> lines, CancellationToken cancellationToken)
    {
        var request = new P.CommitRequest
        {
            OrderId = orderId,
            CustomerId = customerId,
            TotalCents = totalCents
        };
        request.Lines.Add(lines.Select(ToProtoLine));
        await client.CommitAsync(request, cancellationToken: cancellationToken);
    }

    public async Task RevertAsync(string orderId, string customerId, long totalCents, CancellationToken cancellationToken)
    {
        await client.RevertAsync(new P.RevertRequest
        {
            OrderId = orderId,
            CustomerId = customerId,
            TotalCents = totalCents
        }, cancellationToken: cancellationToken);
    }

    private static P.QuoteLine ToProtoLine(QuoteLine line) => new()
    {
        OfferId = line.OfferId,
        BasePriceCents = line.BasePriceCents,
        Quantity = line.Quantity,
        Available = line.Available,
        Capacity = line.Capacity
    };
}
